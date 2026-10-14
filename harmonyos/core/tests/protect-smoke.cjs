const assert = require('node:assert/strict');
const fs = require('node:fs');
const native = require(process.argv[2]);

(async () => {
  assert.equal(native.available(), true);
  let descriptor = -1;
  let calls = 0;
  let reply = JSON.parse(await native.start('/host-test', 1, async fd => {
    descriptor = fd; calls++;
    assert.equal(fs.fstatSync(fd).isSocket(), true);
    await new Promise(resolve => setTimeout(resolve, 30));
    assert.equal(fs.fstatSync(fd).isSocket(), true);
  }));
  assert.equal(reply.ok, true); assert.equal(calls, 1);
  assert.throws(() => fs.fstatSync(descriptor), { code: 'EBADF' });
  assert.equal(JSON.parse(await native.start('/host-test', 1, async () => {})).ok, false);
  assert.equal(JSON.parse(await native.stop()).ok, true);
  for (const callback of [
    async () => { throw new Error('OS protect denied'); },
    () => { throw new Error('synchronous error'); },
    () => undefined
  ]) {
    reply = JSON.parse(await native.start('/host-test', 1, callback));
    assert.equal(reply.ok, false, 'protect must resolve a Promise before dialing');
  }
  let finishLate;
  reply = JSON.parse(await native.start('/host-test', 1, fd => {
    descriptor = fd;
    return new Promise(resolve => { finishLate = resolve; });
  }));
  assert.equal(reply.ok, false, 'unresolved protection must time out');
  // Its duplicate stays alive until the delayed OS operation settles; the
  // descriptor can never be reused for an unrelated socket in that interval.
  assert.equal(fs.fstatSync(descriptor).isSocket(), true);
  finishLate();
  await new Promise(resolve => setTimeout(resolve, 30));
  assert.throws(() => fs.fstatSync(descriptor), { code: 'EBADF' });
  assert.equal(JSON.parse(await native.stop()).ok, true);
  console.log('N-API protect resolve/reject/throw/timeout/FD ownership: passed');
})().catch(error => { console.error(error); process.exitCode = 1; });
