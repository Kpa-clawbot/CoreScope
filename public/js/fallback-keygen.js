function toHex(bytes) {
    return Array.from(bytes)
        .map((b) => b.toString(16).padStart(2, '0'))
        .join('')
        .toUpperCase();
}

function pointToBytes(point) {
    if (point instanceof Uint8Array) {
        return point;
    }
    if (point && typeof point.toRawBytes === 'function') {
        return point.toRawBytes();
    }
    if (point && typeof point.toBytes === 'function') {
        return point.toBytes();
    }
    if (point && point.x !== undefined && point.y !== undefined) {
        const bytes = new Uint8Array(32);
        const y = point.y;
        const x = point.x;
        for (let i = 0; i < 31; i++) {
            bytes[i] = Number((y >> BigInt(8 * i)) & 255n);
        }
        bytes[31] = Number((x & 1n) << 7);
        return bytes;
    }
    throw new Error('Unsupported public key format from noble-ed25519');
}

function buildScalarBigInt(clampedScalar) {
    let scalar = 0n;
    for (let i = 0; i < 32; i++) {
        scalar += BigInt(clampedScalar[i]) << BigInt(8 * i);
    }
    return scalar;
}

async function generateMeshCoreKeypair(nobleEd25519) {
    const seed = crypto.getRandomValues(new Uint8Array(32));
    const digest = await crypto.subtle.digest('SHA-512', seed);
    const digestArray = new Uint8Array(digest);

    const clamped = new Uint8Array(digestArray.slice(0, 32));
    clamped[0] &= 248;
    clamped[31] &= 63;
    clamped[31] |= 64;

    let publicKeyPoint;
    try {
        const scalar = buildScalarBigInt(clamped);
        publicKeyPoint = nobleEd25519.Point.BASE.multiply(scalar);
    } catch (error) {
        try {
            publicKeyPoint = await nobleEd25519.getPublicKey(clamped);
        } catch (fallbackError) {
            publicKeyPoint = nobleEd25519.getPublicKey(clamped);
        }
    }

    const publicKeyBytes = pointToBytes(publicKeyPoint);
    const privateKeyBytes = new Uint8Array(64);
    privateKeyBytes.set(clamped, 0);
    privateKeyBytes.set(digestArray.slice(32, 64), 32);

    return {
        publicKey: toHex(publicKeyBytes),
        privateKey: toHex(privateKeyBytes)
    };
}

export async function searchVanityKey(options) {
    const {
        targetPrefix,
        batchSize = 256,
        getNobleEd25519,
        shouldStop,
        onAttempted
    } = options;

    if (typeof getNobleEd25519 !== 'function') {
        throw new Error('getNobleEd25519 callback is required');
    }

    const nobleEd25519 = getNobleEd25519();
    if (!nobleEd25519) {
        throw new Error('noble-ed25519 is not initialized');
    }

    const normalizedPrefix = targetPrefix.toUpperCase();
    const effectiveBatchSize = Math.max(32, batchSize | 0);

    while (!shouldStop()) {
        for (let i = 0; i < effectiveBatchSize; i++) {
            if (shouldStop()) {
                return null;
            }

            const keypair = await generateMeshCoreKeypair(nobleEd25519);
            if (typeof onAttempted === 'function') {
                onAttempted(1);
            }

            const keyPrefix = keypair.publicKey.slice(0, 2);
            if (keyPrefix === '00' || keyPrefix === 'FF') {
                continue;
            }

            if (keypair.publicKey.startsWith(normalizedPrefix)) {
                return keypair;
            }
        }

        // Yield to keep the UI responsive while brute-forcing in JS.
        await new Promise((resolve) => setTimeout(resolve, 0));
    }

    return null;
}
