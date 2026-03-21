export interface GpuBruteForceResult {
    found: boolean;
    roomName?: string;
    key?: string;
    candidateIndices?: number[];
}
export declare class GpuBruteForce {
    private device;
    private pipeline;
    private bindGroupLayout;
    private paramsBuffer;
    private matchCountBuffer;
    private matchIndicesBuffer;
    private ciphertextBuffer;
    private ciphertextBufferSize;
    private matchCountReadBuffers;
    private matchIndicesReadBuffers;
    private currentReadBufferIndex;
    private bindGroup;
    private bindGroupDirty;
    private static readonly ZERO_DATA;
    private shaderCode;
    init(): Promise<boolean>;
    isAvailable(): boolean;
    indexToRoomName(idx: number, length: number): string | null;
    countNamesForLength(len: number): number;
    runBatch(targetChannelHash: number, nameLength: number, batchOffset: number, batchSize: number, ciphertextHex?: string, targetMacHex?: string): Promise<number[]>;
    destroy(): void;
}
/**
 * Check if WebGPU is supported in the current browser.
 */
export declare function isWebGpuSupported(): boolean;
//# sourceMappingURL=gpu-bruteforce.d.ts.map