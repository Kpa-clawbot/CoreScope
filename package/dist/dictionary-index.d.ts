/**
 * DictionaryIndex - Precomputed hash-indexed wordlist for O(1) lookup
 *
 * Instead of checking every word in the dictionary for each packet,
 * we precompute the channel hash for each word and group them by hash.
 * This reduces dictionary lookup from O(n) to O(n/256) on average.
 */
export interface IndexedWord {
    word: string;
    key: string;
}
export declare class DictionaryIndex {
    private byHash;
    private totalWords;
    constructor();
    /**
     * Build index from wordlist. This precomputes keys and channel hashes.
     * @param words - Array of room names (without # prefix)
     * @param onProgress - Optional progress callback
     */
    build(words: string[], onProgress?: (indexed: number, total: number) => void): void;
    /**
     * Look up all words matching a channel hash byte.
     * @param channelHash - The target channel hash (0-255)
     * @returns Array of words and their precomputed keys
     */
    lookup(channelHash: number): IndexedWord[];
    /**
     * Get the total number of indexed words.
     */
    size(): number;
    /**
     * Get statistics about the index distribution.
     */
    getStats(): {
        total: number;
        buckets: number;
        avgPerBucket: number;
        maxBucket: number;
    };
}
//# sourceMappingURL=dictionary-index.d.ts.map