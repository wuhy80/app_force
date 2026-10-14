const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const native = require(process.argv[2]);

(async () => {
  assert.equal(native.available(), true, 'Go shared library must load');
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'appforce-native-'));
  try {
    const call = async request => JSON.parse(await native.call(JSON.stringify({ directory, ...request })));
    let reply = await call({ method: 'initialize', self_bundle: 'com.appforce.mobile' });
    assert.equal(reply.ok, true);
    reply = await call({ method: 'import', content: JSON.stringify({ outbounds: [
      { type: 'socks', tag: 'ABI fixture', server: '127.0.0.1', server_port: 1090, password: 'private-fixture' }
    ] }) });
    assert.equal(reply.ok, true);
    assert.equal(reply.imported, 1);
    assert.equal(JSON.stringify(reply.state).includes('private-fixture'), false);
    reply = await call({ method: 'saveApplication', application: { bundle_name: 'com.example.browser', name: 'Browser', enabled: true } });
    assert.equal(reply.ok, true);
    reply = await call({ method: 'prepareVpn' });
    assert.equal(reply.ok, true);
    assert.deepEqual(reply.vpn.applications, ['com.example.browser']);
    // stdout is not a TUN; the actual platform must reject it and release its lock.
    reply = JSON.parse(await native.start(directory, 1, async () => {}));
    assert.equal(reply.ok, false);
    assert.equal((await call({ method: 'state' })).state.runtime.active, false);
    assert.equal(JSON.parse(await native.stop()).ok, true);
    assert.throws(() => native.call('bad\0request'), /请求格式无效/);
    assert.equal(JSON.parse(await native.call('{bad json')).ok, false);
    console.log('Go shared library / N-API JSON ABI: passed');
  } finally { fs.rmSync(directory, { recursive: true, force: true }); }
})().catch(error => { console.error(error); process.exitCode = 1; });
