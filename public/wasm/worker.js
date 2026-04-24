import init, { generate_batch } from './pkg/meshcore_keygen.js';

let wasmReady = false;
let running = false;
let currentJobId = 0;

async function ensureInit() {
    if (!wasmReady) {
        await init();
        wasmReady = true;
    }
}

function toHex(bytes) {
    return Array.from(bytes)
        .map(b => b.toString(16).padStart(2, '0'))
        .join('')
        .toUpperCase();
}

function parsePrefix(targetPrefix) {
    const prefix = targetPrefix.toUpperCase();
    const nibbles = prefix.length;
    const byteLen = Math.ceil(nibbles / 2);
    const prefixBytes = new Uint8Array(byteLen);
    for (let i = 0; i < nibbles; i++) {
        const nibble = parseInt(prefix[i], 16);
        if (i & 1) {
            prefixBytes[i >>> 1] |= nibble;
        } else {
            prefixBytes[i >>> 1] = nibble << 4;
        }
    }
    return { prefixBytes, nibbles };
}

function decodeBatchResult(resultBuf) {
    const view = new DataView(resultBuf.buffer, resultBuf.byteOffset, resultBuf.byteLength);
    const matchCount = view.getUint32(0, true);
    const attempted = view.getUint32(4, true);

    const results = [];
    let offset = 8;
    for (let i = 0; i < matchCount; i++) {
        const pubkey = resultBuf.slice(offset, offset + 32);
        offset += 32;
        const clamped = resultBuf.slice(offset, offset + 32);
        offset += 32;
        const sha512SecondHalf = resultBuf.slice(offset, offset + 32);
        offset += 32;
        offset += 32; // seed (currently not needed by JS)

        // Build 64-byte private key: [clamped_scalar][sha512_second_half]
        const privateKey = new Uint8Array(64);
        privateKey.set(clamped, 0);
        privateKey.set(sha512SecondHalf, 32);

        results.push({
            publicKey: toHex(pubkey),
            privateKey: toHex(privateKey),
            publicKeyBytes: Array.from(pubkey),
            privateKeyBytes: Array.from(privateKey),
            matches: true
        });
    }

    return { attempted, results };
}

function clamp(value, min, max) {
    return Math.max(min, Math.min(max, value));
}

async function runSearchLoop(config) {
    await ensureInit();

    running = true;
    const localRunId = ++currentJobId;
    const jobId = config.jobId ?? localRunId;
    const { prefixBytes, nibbles } = parsePrefix(config.targetPrefix);

    let batchSize = config.batchSize;
    let attemptedTotal = 0;
    let reportedAttempted = 0;
    let batchCount = 0;
    let totalWasmMs = 0;
    let totalBatchWallMs = 0;
    let lastProgressAt = performance.now();

    while (running && localRunId === currentJobId) {
        const batchStart = performance.now();
        const resultBuf = generate_batch(prefixBytes, nibbles, batchSize);
        const wasmDoneAt = performance.now();
        const decoded = decodeBatchResult(resultBuf);
        const batchEnd = performance.now();

        const wasmMs = wasmDoneAt - batchStart;
        const batchWallMs = batchEnd - batchStart;
        const overheadMs = batchWallMs - wasmMs;

        totalWasmMs += wasmMs;
        totalBatchWallMs += batchWallMs;
        attemptedTotal += decoded.attempted;
        batchCount += 1;

        // If there is a match, stop this worker loop immediately.
        if (decoded.results.length > 0) {
            const attemptedDelta = attemptedTotal - reportedAttempted;
            reportedAttempted = attemptedTotal;
            self.postMessage({
                type: 'match',
                jobId,
                result: decoded.results[0],
                attemptedDelta,
                attemptedTotal,
                metrics: {
                    batchSize,
                    wasmMs,
                    batchWallMs,
                    overheadMs,
                    batchCount,
                    totalWasmMs,
                    totalBatchWallMs
                }
            });
            running = false;
            break;
        }

        const now = batchEnd;
        if (now - lastProgressAt >= config.progressIntervalMs) {
            const attemptedDelta = attemptedTotal - reportedAttempted;
            reportedAttempted = attemptedTotal;
            self.postMessage({
                type: 'progress',
                jobId,
                attemptedDelta,
                attemptedTotal,
                metrics: {
                    batchSize,
                    wasmMs,
                    batchWallMs,
                    overheadMs,
                    batchCount,
                    totalWasmMs,
                    totalBatchWallMs
                }
            });
            lastProgressAt = now;
        }

        if (config.adaptiveBatching) {
            const targetMs = Math.max(1, config.targetBatchMs);
            const scale = targetMs / Math.max(wasmMs, 0.1);
            const boundedScale = clamp(scale, 0.5, 2.0);
            batchSize = clamp(
                Math.round(batchSize * boundedScale),
                config.minBatchSize,
                config.maxBatchSize
            );
        }

        // Yield occasionally so stop messages are handled quickly.
        if ((batchCount & 7) === 0) {
            await new Promise(resolve => setTimeout(resolve, 0));
        }
    }

    if (attemptedTotal > reportedAttempted) {
        self.postMessage({
            type: 'progress',
            jobId,
            attemptedDelta: attemptedTotal - reportedAttempted,
            attemptedTotal,
            metrics: {
                batchSize,
                wasmMs: 0,
                batchWallMs: 0,
                overheadMs: 0,
                batchCount,
                totalWasmMs,
                totalBatchWallMs
            }
        });
    }

    self.postMessage({
        type: 'stopped',
        jobId,
        attemptedTotal,
        metrics: {
            batchCount,
            totalWasmMs,
            totalBatchWallMs
        }
    });
}

async function runSingleBatch(targetPrefix, batchSize) {
    await ensureInit();
    const { prefixBytes, nibbles } = parsePrefix(targetPrefix);
    const resultBuf = generate_batch(prefixBytes, nibbles, batchSize);
    const decoded = decodeBatchResult(resultBuf);
    self.postMessage({ type: 'results', results: decoded.results, attempted: decoded.attempted });
}

self.onmessage = async function(e) {
    const { type } = e.data;

    if (type === 'start') {
        running = false;
        await runSearchLoop({
            jobId: e.data.jobId,
            targetPrefix: e.data.targetPrefix,
            batchSize: e.data.batchSize,
            adaptiveBatching: Boolean(e.data.adaptiveBatching),
            targetBatchMs: e.data.targetBatchMs ?? 16,
            minBatchSize: e.data.minBatchSize ?? 512,
            maxBatchSize: e.data.maxBatchSize ?? 65536,
            progressIntervalMs: e.data.progressIntervalMs ?? 150
        });
    } else if (type === 'stop') {
        running = false;
        currentJobId += 1;
    } else if (type === 'generate') {
        // Backward-compatible one-shot mode.
        await runSingleBatch(e.data.targetPrefix, e.data.batchSize);
    }
};
