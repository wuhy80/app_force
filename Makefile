TAGS = with_quic,with_utls

.PHONY: frontend build desktop windows test check clean
frontend:
	npm --prefix frontend ci
	npm --prefix frontend run build

build: frontend
	go build -trimpath -tags $(TAGS) -o bin/appforce ./cmd/appforce

desktop: frontend
	go build -trimpath -tags desktop,production,$(TAGS) -o bin/appforce-desktop ./cmd/appforce

windows: frontend
	go run ./build/windows
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -tags desktop,production,$(TAGS) -ldflags "-H windowsgui" -o bin/AppForce.exe ./cmd/appforce

test:
	go test -race -tags $(TAGS) ./...

check:
	go vet -tags $(TAGS) ./...
	npm --prefix frontend run build

clean:
	rm -rf bin
	rm -f cmd/appforce/rsrc_windows_*.syso
