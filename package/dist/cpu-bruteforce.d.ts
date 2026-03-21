/**
 * CPU-based brute force implementation.
 * Much slower than GPU but works everywhere.
 */
export declare class CpuBruteForce {
    /**
     * Run a batch of candidates on CPU.
     * Returns indices of candidates that match the channel hash and MAC.
     */
    runBatch(targetChannelHash: number, nameLength: number, batchOffset: number, batchSize: number, ciphertextHex?: string, targetMacHex?: string): number[];
    destroy(): void;
}
//# sourceMappingURL=cpu-bruteforce.d.ts.map