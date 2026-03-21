/**
 * GroupTextCracker - Standalone MeshCore GroupText packet cracker
 *
 * Cracks encrypted GroupText packets by trying room names until the
 * correct encryption key is found.
 */
import type { CrackOptions, CrackResult, ProgressCallback, DecodedPacket } from './types.js';
/**
 * Main cracker class for MeshCore GroupText packets.
 */
export declare class GroupTextCracker {
    private gpuInstance;
    private gpuWordPairs;
    private cpuInstance;
    private wordlist;
    private abortFlag;
    private useTimestampFilter;
    private useUtf8Filter;
    private useSenderFilter;
    private validSeconds;
    private useCpu;
    /**
     * Load a wordlist from a URL for dictionary attacks.
     * The wordlist should be a text file with one word per line.
     *
     * @param url - URL to fetch the wordlist from
     */
    loadWordlist(url: string): Promise<void>;
    /**
     * Set the wordlist directly from an array of words.
     *
     * @param words - Array of room names to try
     */
    setWordlist(words: string[]): void;
    /**
     * Abort the current cracking operation.
     * The crack() method will return with aborted: true.
     */
    abort(): void;
    /**
     * Check if WebGPU is available in the current environment.
     */
    isGpuAvailable(): boolean;
    /**
     * Decode a packet and extract the information needed for cracking.
     * Delegates to MeshCorePacketDecoder which handles both single-byte
     * and multi-byte path hops (v1.11+ path_len encoding).
     *
     * @param packetHex - The packet data as a hex string
     * @returns Decoded packet info or null if not a GroupText packet
     */
    decodePacket(packetHex: string): Promise<DecodedPacket | null>;
    /**
     * Crack a GroupText packet to find the room name and decrypt the message.
     *
     * @param packetHex - The packet data as a hex string
     * @param options - Cracking options
     * @param onProgress - Optional callback for progress updates
     * @returns The cracking result
     */
    crack(packetHex: string, options?: CrackOptions, onProgress?: ProgressCallback): Promise<CrackResult>;
    /**
     * Clean up resources.
     * Call this when you're done using the cracker.
     */
    destroy(): void;
}
//# sourceMappingURL=cracker.d.ts.map