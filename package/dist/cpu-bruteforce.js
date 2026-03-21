// CPU-based brute force key cracking for MeshCore packets
// Fallback for environments without WebGPU support
import { indexToRoomName, deriveKeyFromRoomName, getChannelHash, verifyMac, } from './core.js';
/**
 * CPU-based brute force implementation.
 * Much slower than GPU but works everywhere.
 */
export class CpuBruteForce {
    /**
     * Run a batch of candidates on CPU.
     * Returns indices of candidates that match the channel hash and MAC.
     */
    runBatch(targetChannelHash, nameLength, batchOffset, batchSize, ciphertextHex, targetMacHex) {
        const matches = [];
        const targetHashHex = targetChannelHash.toString(16).padStart(2, '0');
        const verifyMacEnabled = !!(ciphertextHex && targetMacHex);
        for (let i = 0; i < batchSize; i++) {
            const nameIdx = batchOffset + i;
            const roomName = indexToRoomName(nameLength, nameIdx);
            if (!roomName) {
                continue; // Invalid index (e.g., consecutive dashes)
            }
            // Derive key from room name (with # prefix)
            const key = deriveKeyFromRoomName('#' + roomName);
            // Check channel hash
            const channelHash = getChannelHash(key);
            if (channelHash !== targetHashHex) {
                continue;
            }
            // Channel hash matches - verify MAC if enabled
            if (verifyMacEnabled) {
                if (!verifyMac(ciphertextHex, targetMacHex, key)) {
                    continue;
                }
            }
            // Found a match
            matches.push(nameIdx);
        }
        return matches;
    }
    destroy() {
        // No resources to clean up for CPU implementation
    }
}
//# sourceMappingURL=cpu-bruteforce.js.map