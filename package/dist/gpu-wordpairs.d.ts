export declare class GpuWordPairs {
    private device;
    private pipeline;
    private bindGroupLayout;
    private paramsBuffer;
    private matchCountBuffer;
    private matchIBuffer;
    private matchJBuffer;
    private ciphertextBuffer;
    private wordDataBuffer;
    private wordOffsetsBuffer;
    private matchCountReadBuffer;
    private matchIReadBuffer;
    private matchJReadBuffer;
    private wordCount;
    private ciphertextBufferSize;
    private static readonly ZERO_DATA;
    private shaderCode;
    init(): Promise<boolean>;
    /**
     * Upload the word list to GPU buffers.
     * Words are packed into a byte buffer, with offsets stored separately.
     */
    uploadWords(words: string[]): void;
    /**
     * Run a batch of word pair checks on the GPU.
     * @param targetChannelHash - Target channel hash byte
     * @param batchOffset - Starting pair index (i * wordCount + j)
     * @param batchSize - Number of pairs to check
     * @param ciphertextHex - Ciphertext for MAC verification
     * @param targetMacHex - Target MAC
     * @returns Array of matching pair indices as [i, j] tuples
     */
    runBatch(targetChannelHash: number, batchOffset: number, batchSize: number, ciphertextHex: string, targetMacHex: string): Promise<Array<[number, number]>>;
    getWordCount(): number;
    destroy(): void;
}
//# sourceMappingURL=gpu-wordpairs.d.ts.map