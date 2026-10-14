package traffic

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"appforce/internal/model"
	"github.com/sagernet/sing-box/adapter"
	tun "github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
)

type outbound struct {
	adapter.Outbound
	tag string
}

func (o outbound) Tag() string { return o.tag }

func openTest(t *testing.T) *Recorder {
	t.Helper()
	r, err := Open(filepath.Join(t.TempDir(), "traffic.db"))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func reportTest(t *testing.T, r *Recorder, days int, appID string, direct bool) Report {
	t.Helper()
	report, err := r.Report(days, appID, direct)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func metadata(programPath string, pid uint32) adapter.InboundContext {
	return adapter.InboundContext{InboundType: "tun", ProcessInfo: &adapter.ConnectionOwner{ProcessPath: programPath, ProcessID: pid}}
}

func TestProgramNodeTrafficIsCountedAsProxy(t *testing.T) {
	r := openTest(t)
	tracker := NewTracker(r, nil)
	defer tracker.Close()
	for _, tag := range []string{"selected", model.NodeOutboundTag("program-node"), "direct"} {
		flow := tracker.RoutedFlow(context.Background(), metadata("/apps/browser", 12), nil, outbound{tag: tag})
		flow.AttachFlow(nil)
		flow.CountForward(100)
		flow.CountReverse(200)
		flow.CloseFlow(0)
	}
	proxied := reportTest(t, r, 7, "", false)
	if proxied.Summary.Upload != 200 || proxied.Summary.Download != 400 || proxied.Summary.ActiveConnections != 0 {
		t.Fatalf("program node traffic was not included in proxy totals: %+v", proxied.Summary)
	}
	all := reportTest(t, r, 7, "", true)
	if all.Summary.Upload != 300 || all.Summary.Download != 600 {
		t.Fatalf("proxy and direct traffic were double counted: %+v", all.Summary)
	}
}

func TestShortFlowsGroupedByProgramSurviveReopenAndDailyBoundaries(t *testing.T) {
	r := openTest(t)
	now := time.Date(2026, 10, 14, 23, 59, 59, 0, time.FixedZone("CST", 8*3600))
	r.now = func() time.Time { return now }
	rule := model.Rule{ID: "old-rule-id", Name: "Antigravity", Path: `C:\Apps\Antigravity\Antigravity.exe`, Enabled: true}
	tracker := NewTracker(r, []model.Rule{rule})
	id := Identity(rule.Path)
	// More short-lived flows than either the UI limit or the builtin closed history limit.
	for i := 0; i < 1500; i++ {
		p := `C:\Apps\Antigravity\Antigravity.exe`
		if i%2 == 0 {
			p = `c:\apps\ANTIGRAVITY\resources\app\extensions\language_server_windows_x64.exe`
		}
		flow := tracker.RoutedFlow(context.Background(), metadata(p, uint32(i+1)), nil, outbound{tag: "selected"})
		flow.AttachFlow(nil)
		flow.CountForward(2)
		flow.CountReverse(3)
		flow.CloseFlow(tun.FlowCloseFinished)
		flow.CloseFlow(tun.FlowCloseFinished)
	}
	now = now.Add(time.Second)
	r.Sample()
	report := reportTest(t, r, 7, id, false)
	if len(report.Applications) != 1 || report.Summary.LifetimeUpload != 3000 || report.Summary.LifetimeDownload != 4500 || report.Summary.ActiveConnections != 0 {
		t.Fatalf("short flow accounting or grouping failed: %+v", report)
	}
	if report.Summary.UploadRate != 3000 || report.Summary.DownloadRate != 4500 {
		t.Fatalf("wrong rates: %+v", report.Summary)
	}
	if report.History[5].Upload != 3000 || report.History[6].Upload != 0 {
		t.Fatalf("wrong device calendar boundary: %+v", report.History)
	}
	flow := tracker.RoutedFlow(context.Background(), metadata(rule.Path, 8000), nil, outbound{tag: "selected"})
	flow.AttachFlow(nil)
	flow.CountReverse(100)
	flow.CloseFlow(tun.FlowCloseFinished)
	today := reportTest(t, r, 1, id, false)
	if today.Summary.Download != 100 || today.Summary.LifetimeDownload != 4600 {
		t.Fatalf("period and lifetime mixed: %+v", today)
	}
	now = now.Add(time.Second)
	r.Sample()
	if reportTest(t, r, 1, id, false).Summary.UploadRate != 0 {
		t.Fatal("idle upload rate did not decay")
	}
	tracker.Close()
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(r.file)
	if err != nil {
		t.Fatal(err)
	}
	reopened.now = r.now
	saved := reportTest(t, reopened, 7, id, false)
	if saved.Summary.LifetimeDownload != 4600 || saved.Summary.Download != 4600 || saved.Summary.UploadRate != 0 || saved.Summary.ActiveConnections != 0 || saved.SavedAt == nil || !saved.CollectedSince.Equal(report.CollectedSince) {
		t.Fatalf("history was not restored: %+v", saved)
	}
	rule.ID = "replacement-rule-id"
	rule.Path = `c:/apps/antigravity/ANTIGRAVITY.exe`
	tracker = NewTracker(reopened, []model.Rule{rule})
	flow = tracker.RoutedFlow(context.Background(), metadata(rule.Path, 9000), nil, outbound{tag: "selected"})
	flow.AttachFlow(nil)
	flow.CountReverse(7)
	tracker.Close()
	flow.CountReverse(999)
	flow.CloseFlow(tun.FlowCloseFinished)
	saved = reportTest(t, reopened, 7, id, false)
	if len(saved.Applications) != 1 || saved.Summary.LifetimeDownload != 4607 || saved.Summary.ActiveConnections != 0 {
		t.Fatalf("restart changed identity or accepted stale counters: %+v", saved)
	}
}

func TestProxyDirectAppFiltersAndUnknownSources(t *testing.T) {
	r := openTest(t)
	tracker := NewTracker(r, nil)
	defer tracker.Close()
	for _, row := range []struct {
		path, tag string
		bytes     int
	}{
		{"/opt/editor", "selected", 10}, {"/opt/editor", "direct", 20}, {"/opt/browser", "direct", 30},
	} {
		flow := tracker.RoutedFlow(context.Background(), metadata(row.path, 10), nil, outbound{tag: row.tag})
		flow.AttachFlow(nil)
		flow.CountReverse(row.bytes)
		flow.CloseFlow(tun.FlowCloseFinished)
	}
	proxy := reportTest(t, r, 7, "", false)
	all := reportTest(t, r, 7, "", true)
	if proxy.Summary.Download != 10 || len(proxy.Applications) != 1 || all.Summary.Download != 60 || len(all.Applications) != 2 {
		t.Fatalf("scope filter failed: %+v / %+v", proxy, all)
	}
	selected := reportTest(t, r, 7, Identity("/opt/editor"), true)
	if selected.Summary.Download != 30 || selected.History[6].Download != 30 || len(selected.Applications) != 2 {
		t.Fatalf("app filter failed: %+v", selected)
	}
	for _, inbound := range []string{"mixed", "tun"} {
		flow := tracker.RoutedFlow(context.Background(), adapter.InboundContext{InboundType: inbound}, nil, outbound{tag: "selected"})
		flow.CountForward(1)
		flow.CloseFlow(tun.FlowCloseFinished)
	}
	if len(reportTest(t, r, 7, "", false).Applications) != 3 {
		t.Fatal("unknown sources were not kept separate")
	}
	if _, err := r.Report(0, "", false); err == nil {
		t.Fatal("accepted invalid days")
	}
	if _, err := r.Report(366, "", false); err == nil {
		t.Fatal("accepted oversized period")
	}
	if _, err := r.Report(7, "missing", false); err == nil {
		t.Fatal("accepted missing app")
	}
}

func TestTCPCountsActualBytesAndCloseOnce(t *testing.T) {
	r := openTest(t)
	tracker := NewTracker(r, nil)
	defer tracker.Close()
	left, right := net.Pipe()
	defer right.Close()
	conn := tracker.RoutedConnection(context.Background(), left, metadata("/bin/app", 1), nil, outbound{tag: "selected"})
	done := make(chan error, 1)
	go func() { _, err := right.Write([]byte("upload")); done <- err }()
	if _, err := io.ReadFull(conn, make([]byte, 6)); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	go func() { _, err := io.ReadFull(right, make([]byte, 8)); done <- err }()
	if _, err := conn.Write([]byte("download")); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	before := reportTest(t, r, 1, "", false)
	if before.Summary.Upload != 6 || before.Summary.Download != 8 || before.Summary.ActiveConnections != 1 {
		t.Fatalf("TCP directions incorrect: %+v", before)
	}
	_ = conn.Close()
	_ = conn.Close()
	if n, _ := conn.Write([]byte("failed")); n != 0 {
		t.Fatal("expected closed pipe")
	}
	after := reportTest(t, r, 1, "", false)
	if after.Summary.Upload != 6 || after.Summary.Download != 8 || after.Summary.ActiveConnections != 0 {
		t.Fatalf("close or failed write was counted: %+v", after)
	}
}

func TestParentApplicationWinsOverSavedHelperRule(t *testing.T) {
	r := openTest(t)
	parent := model.Rule{Name: "Antigravity", Path: `C:\Apps\Antigravity\Antigravity.exe`, Enabled: true}
	helper := model.Rule{Name: "Language server", Path: `C:\Apps\Antigravity\resources\language_server_windows_x64.exe`, Enabled: true}
	tracker := NewTracker(r, []model.Rule{helper, parent})
	defer tracker.Close()
	flow := tracker.RoutedFlow(context.Background(), metadata(helper.Path, 123), nil, outbound{tag: "selected"})
	flow.AttachFlow(nil)
	flow.CountReverse(12)
	flow.CloseFlow(tun.FlowCloseFinished)
	if got := reportTest(t, r, 1, Identity(parent.Path), false).Summary.Download; got != 12 {
		t.Fatalf("helper was not grouped under parent: %d", got)
	}
	if got := reportTest(t, r, 1, Identity(helper.Path), false).Summary.Download; got != 0 {
		t.Fatalf("helper was double counted: %d", got)
	}
}

func TestPersistenceFailureRetriesLatestValuesWithoutDoubling(t *testing.T) {
	r := openTest(t)
	id := r.Register("test", "/test")
	r.count(id, true, 10, 20)
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(r.file)
	if err := os.Rename(dir, dir+"-offline"); err != nil {
		t.Fatal(err)
	}
	r.count(id, true, 1, 2)
	if err := r.Flush(); err == nil {
		t.Fatal("disk failure not reported")
	}
	if reportTest(t, r, 1, "", false).StorageError == "" {
		t.Fatal("storage failure hidden")
	}
	r.count(id, true, 3, 4)
	if err := os.Rename(dir+"-offline", dir); err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(r.file)
	if err != nil {
		t.Fatal(err)
	}
	got := reportTest(t, reopened, 1, "", false)
	if got.StorageError != "" || got.Summary.Upload != 14 || got.Summary.Download != 26 || got.History[0].Download != 26 {
		t.Fatalf("retry lost or duplicated bytes: %+v", got)
	}
}

func TestUDPCountsBothDirectionsAndPreservesClosedTraffic(t *testing.T) {
	r := openTest(t)
	tracker := NewTracker(r, nil)
	defer tracker.Close()
	local, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	peer, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	_ = local.SetDeadline(time.Now().Add(3 * time.Second))
	_ = peer.SetDeadline(time.Now().Add(3 * time.Second))
	conn := tracker.RoutedPacketConnection(context.Background(), bufio.NewPacketConn(local), metadata("/opt/udp-app", 1), nil, outbound{tag: "selected"})
	if _, err := peer.WriteTo([]byte("request"), local.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	buffer := buf.NewSize(100)
	defer buffer.Release()
	if _, err := conn.ReadPacket(buffer); err != nil {
		t.Fatal(err)
	}
	if err := conn.WritePacket(buf.As([]byte("response")), M.SocksaddrFromNet(peer.LocalAddr())); err != nil {
		t.Fatal(err)
	}
	if _, _, err := peer.ReadFrom(make([]byte, 100)); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	_ = conn.Close()
	report := reportTest(t, r, 1, "", false)
	if report.Summary.Upload != 7 || report.Summary.Download != 8 || report.Summary.ActiveConnections != 0 {
		t.Fatalf("UDP accounting failed: %+v", report)
	}
}

func TestConcurrentCountersReportsAndFlushes(t *testing.T) {
	r := openTest(t)
	id := r.Register("parallel", "/parallel")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				r.count(id, true, 1, 2)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			r.Sample()
			_, _ = r.Report(7, "", false)
			if err := r.Flush(); err != nil {
				t.Error(err)
			}
		}
	}()
	wg.Wait()
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(r.file)
	if err != nil {
		t.Fatal(err)
	}
	if got := reportTest(t, reopened, 7, "", false).Summary; got.Upload != 8000 || got.Download != 16000 {
		t.Fatalf("concurrent counters lost: %+v", got)
	}
}
