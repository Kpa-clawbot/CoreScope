const assert = require('assert');
const fs = require('fs');

const src = fs.readFileSync('public/perf.js', 'utf8');

function mustInclude(text, label) {
  assert.ok(src.includes(text), `missing ${label}: ${text}`);
}

mustInclude("category: 'I/O'", 'I/O chart group');
mustInclude("id: 'diskio'", 'disk throughput chart');
mustInclude("id: 'iosyscalls'", 'syscall chart');
mustInclude("id: 'sqlitewal'", 'SQLite WAL chart');
mustInclude("id: 'sqlitecache'", 'SQLite cache chart');
mustInclude("id: 'writerates'", 'write source rates chart');

mustInclude("key: 'ioReadBps'", 'read throughput dataset');
mustInclude("key: 'ioWriteBps'", 'write throughput dataset');
mustInclude("key: 'ioSyscallsRead'", 'read syscall dataset');
mustInclude("key: 'ioSyscallsWrite'", 'write syscall dataset');
mustInclude("key: 'sqlitePerfWalMB'", 'SQLite WAL dataset');
mustInclude("key: 'sqliteCacheHitPct'", 'SQLite cache hit dataset');
mustInclude("key: 'writeTxRate'", 'tx write rate dataset');
mustInclude("key: 'writeObsRate'", 'observation write rate dataset');
mustInclude("key: 'writeErrorRate'", 'write error rate dataset');

mustInclude('function writeSourceRates(writeSources)', 'write source rate helper');
mustInclude('pushSample(server, server.observerCounts || null, ioStats, sqliteStats, writeSources)', 'refresh IO sample wiring');

const diskChart = src.match(/id: 'diskio'[\s\S]*?datasets: \[([\s\S]*?)\]\s*\}/);
assert.ok(diskChart, 'diskio chart definition not found');
assert.ok((diskChart[1].match(/key:/g) || []).length >= 2, 'diskio should be multiline');

const syscallChart = src.match(/id: 'iosyscalls'[\s\S]*?datasets: \[([\s\S]*?)\]\s*\}/);
assert.ok(syscallChart, 'iosyscalls chart definition not found');
assert.ok((syscallChart[1].match(/key:/g) || []).length >= 2, 'iosyscalls should be multiline');

const writeRatesChart = src.match(/id: 'writerates'[\s\S]*?datasets: \[([\s\S]*?)\]\s*\}/);
assert.ok(writeRatesChart, 'writerates chart definition not found');
assert.ok((writeRatesChart[1].match(/key:/g) || []).length >= 4, 'writerates should plot multiple counters');

console.log('perf IO graph wiring tests passed');
