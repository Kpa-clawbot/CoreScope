/**
 * Options for configuring the cracking process.
 */
export interface CrackOptions {
    /**
     * Maximum room name length to search (default: 8).
     * Longer names exponentially increase search time.
     */
    maxLength?: number;
    /**
     * Minimum room name length to search (default: 1).
     * Use this to skip shorter room names if you know the target is longer.
     */
    startingLength?: number;
    /**
     * Use dictionary attack before brute force (default: true).
     * When enabled and a wordlist is loaded, tries dictionary words first.
     * Set to false to skip dictionary attack even if a wordlist is loaded.
     */
    useDictionary?: boolean;
    /**
     * Filter results by timestamp validity (default: true).
     * When enabled, rejects results where the decrypted timestamp
     * is outside the validity window.
     */
    useTimestampFilter?: boolean;
    /**
     * Timestamp validity window in seconds (default: 2592000 = 30 days).
     * Only used when useTimestampFilter is enabled.
     * Timestamps older than this many seconds from now are rejected.
     */
    validSeconds?: number;
    /**
     * Filter results by UTF-8 validity (default: true).
     * When enabled, rejects results containing invalid UTF-8 sequences.
     */
    useUtf8Filter?: boolean;
    /**
     * Filter results by sender presence (default: true).
     * When enabled, only accepts results where the decrypted message
     * has a valid sender field, which is expected in valid MeshCore messages.
     *
     * Technically, this checks for ": " (colon-space) within the first 50
     * characters of the decrypted text, where the part before the colon
     * doesn't contain special characters like brackets.
     *
     * When a sender is found, the decrypted message includes the full
     * "sender: message" format.
     */
    useSenderFilter?: boolean;
    /**
     * Resume cracking from a specific position.
     * Useful for resuming interrupted searches.
     * The interpretation depends on startFromType.
     */
    startFrom?: string;
    /**
     * How to interpret the startFrom value (default: 'bruteforce').
     * - 'dictionary': startFrom is a dictionary word; resume dictionary attack from that word, then continue to word pairs and brute force
     * - 'dictionary-pair': startFrom is "word1+word2"; resume two-word combination attack from that pair, then continue to brute force
     * - 'bruteforce': startFrom is a brute-force position; skip dictionary/pairs and resume brute force from that position
     */
    startFromType?: 'dictionary' | 'dictionary-pair' | 'bruteforce';
    /**
     * Force CPU-based cracking instead of WebGPU (default: false).
     * Much slower but works in environments without WebGPU support.
     * Also useful for testing.
     */
    forceCpu?: boolean;
    /**
     * EXPERIMENTAL: Try two-word combinations from the wordlist (default: false).
     * After the dictionary attack, tries every pair of words concatenated together
     * (e.g., "hello" + "world" = "helloworld") where the combined length is <= 30.
     * This can significantly increase search time depending on wordlist size.
     * Only used when useDictionary is true and a wordlist is loaded.
     */
    useTwoWordCombinations?: boolean;
    /**
     * EXPERIMENTAL - Target GPU dispatch time in milliseconds (default: 1000).
     *
     * Higher values improve throughput by reducing dispatch overhead, but:
     * - Reduce responsiveness of progress updates and abort()
     * - May cause browser watchdog timeouts or "device lost" errors
     * - May cause system UI stuttering during long dispatches
     *
     * Values up to ~10000ms may work on modern GPUs but stability varies
     * by browser, OS, and hardware. Test thoroughly before using in production.
     * Only applies when using GPU (not forceCpu).
     */
    gpuDispatchMs?: number;
}
/**
 * Progress information reported during cracking.
 */
export interface ProgressReport {
    /** Total candidates checked so far */
    checked: number;
    /** Total candidates to check */
    total: number;
    /** Progress percentage (0-100) */
    percent: number;
    /** Current cracking rate in keys/second */
    rateKeysPerSec: number;
    /** Estimated time remaining in seconds */
    etaSeconds: number;
    /** Time elapsed since start in seconds */
    elapsedSeconds: number;
    /** Current room name length being tested */
    currentLength: number;
    /** Current room name position being tested */
    currentPosition: string;
    /** Current phase of cracking */
    phase: 'public-key' | 'wordlist' | 'wordlist-pairs' | 'bruteforce';
}
/**
 * Callback function for progress updates.
 * Called approximately 5 times per second during cracking.
 */
export type ProgressCallback = (report: ProgressReport) => void;
/**
 * Result of a cracking operation.
 */
export interface CrackResult {
    /** Whether a matching room name was found */
    found: boolean;
    /** The room name (without '#' prefix) if found */
    roomName?: string;
    /** The derived encryption key (hex) if found */
    key?: string;
    /** The decrypted message content if found */
    decryptedMessage?: string;
    /** Whether the operation was aborted */
    aborted?: boolean;
    /**
     * Position to resume from to continue searching.
     * Always provided on success, abort, or not-found (not on error).
     * Pass this as `startFrom` with the corresponding `startFromType` to skip
     * past this result and continue searching for additional matches.
     */
    resumeFrom?: string;
    /**
     * Type of resume position. Use as `startFromType` when resuming.
     * - 'dictionary': resumeFrom is a dictionary word
     * - 'dictionary-pair': resumeFrom is "word1+word2" (two-word combination)
     * - 'bruteforce': resumeFrom is a brute-force position
     */
    resumeType?: 'dictionary' | 'dictionary-pair' | 'bruteforce';
    /** Error message if an error occurred */
    error?: string;
}
/**
 * Decoded packet information extracted from a MeshCore GroupText packet.
 */
export interface DecodedPacket {
    /** Channel hash (1 byte, hex) */
    channelHash: string;
    /** Encrypted ciphertext (hex) */
    ciphertext: string;
    /** MAC for verification (2 bytes, hex) */
    cipherMac: string;
    /** Whether this is a GroupText packet */
    isGroupText: boolean;
}
//# sourceMappingURL=types.d.ts.map