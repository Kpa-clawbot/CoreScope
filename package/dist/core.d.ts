export declare const CHARS = "abcdefghijklmnopqrstuvwxyz0123456789";
export declare const CHARS_LEN: number;
export declare const CHARS_WITH_DASH: string;
export declare const PUBLIC_ROOM_NAME = "[[public room]]";
export declare const PUBLIC_KEY = "8b3387e9c5cdea6ac9e5edbaa115cd72";
export declare const DEFAULT_VALID_SECONDS: number;
/**
 * Convert room name to (length, index) for resuming/skipping.
 * Index encoding: LSB-first (first character = least significant digit).
 */
export declare function roomNameToIndex(name: string): {
    length: number;
    index: number;
} | null;
/**
 * Convert (length, index) to room name.
 * Index encoding: LSB-first (first character = least significant digit).
 */
export declare function indexToRoomName(length: number, idx: number): string | null;
/**
 * Derive 128-bit key from room name using SHA256.
 * Room names are prefixed with '#' before hashing.
 */
export declare function deriveKeyFromRoomName(roomName: string): string;
/**
 * Compute channel hash (first byte of SHA256(key)).
 */
export declare function getChannelHash(keyHex: string): string;
/**
 * Verify MAC using HMAC-SHA256 with 32-byte padded key.
 */
export declare function verifyMac(ciphertext: string, cipherMac: string, keyHex: string): boolean;
/**
 * Total index space for a given length (including invalid consecutive-dash indices).
 * This is the full mixed-radix space: 36 * 37^(len-2) * 36 for len >= 3.
 * Use this as the brute-force iteration bound (indexToRoomName returns null for holes).
 */
export declare function indexSpaceForLength(len: number): number;
/**
 * Count valid room names for a given length.
 * Accounts for dash rules (no start/end dash, no consecutive dashes).
 */
export declare function countNamesForLength(len: number): number;
/**
 * Check if timestamp is within the validity window.
 * @param timestamp - Unix timestamp to validate
 * @param validSeconds - Validity window in seconds (default: 30 days)
 * @param now - Current time for testing (default: current time)
 */
export declare function isTimestampValid(timestamp: number, validSeconds?: number, now?: number): boolean;
/**
 * Check for valid UTF-8 (no replacement characters).
 */
export declare function isValidUtf8(text: string): boolean;
/**
 * Check if text contains a colon character.
 */
export declare function hasColon(text: string): boolean;
/**
 * Room name generator - iterates through all valid room names.
 */
export declare class RoomNameGenerator {
    private length;
    private indices;
    private done;
    private currentInLength;
    private totalForLength;
    current(): string;
    getLength(): number;
    getCurrentInLength(): number;
    getTotalForLength(): number;
    getRemainingInLength(): number;
    isDone(): boolean;
    next(): boolean;
    private isValid;
    nextValid(): boolean;
    skipTo(targetLength: number, targetIndex: number): void;
}
//# sourceMappingURL=core.d.ts.map