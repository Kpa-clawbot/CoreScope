// Core logic for MeshCore packet cracker - pure functions
import SHA256 from 'crypto-js/sha256.js';
import HmacSHA256 from 'crypto-js/hmac-sha256.js';
import Hex from 'crypto-js/enc-hex.js';
// Room name character set
export const CHARS = 'abcdefghijklmnopqrstuvwxyz0123456789';
export const CHARS_LEN = CHARS.length; // 36
export const CHARS_WITH_DASH = CHARS + '-';
// Public room special case
export const PUBLIC_ROOM_NAME = '[[public room]]';
export const PUBLIC_KEY = '8b3387e9c5cdea6ac9e5edbaa115cd72';
// Default timestamp validity window (30 days in seconds)
export const DEFAULT_VALID_SECONDS = 30 * 24 * 60 * 60;
/**
 * Convert room name to (length, index) for resuming/skipping.
 * Index encoding: LSB-first (first character = least significant digit).
 */
export function roomNameToIndex(name) {
    if (!name || name.length === 0) {
        return null;
    }
    const length = name.length;
    let index = 0;
    let multiplier = 1;
    // Process from left to right (first char is LSB, matching indexToRoomName)
    let prevWasDash = false;
    for (let i = 0; i < length; i++) {
        const c = name[i];
        const charIdx = CHARS_WITH_DASH.indexOf(c);
        if (charIdx === -1) {
            return null;
        } // Invalid character
        const isFirst = i === 0;
        const isLast = i === length - 1;
        const charCount = isFirst || isLast ? 36 : 37;
        const isDash = charIdx === 36;
        // Dash not allowed at start/end
        if ((isFirst || isLast) && isDash) {
            return null;
        }
        // No consecutive dashes
        if (isDash && prevWasDash) {
            return null;
        }
        prevWasDash = isDash;
        index += charIdx * multiplier;
        multiplier *= charCount;
    }
    return { length, index };
}
/**
 * Convert (length, index) to room name.
 * Index encoding: LSB-first (first character = least significant digit).
 */
export function indexToRoomName(length, idx) {
    if (length <= 0) {
        return null;
    }
    let result = '';
    let remaining = idx;
    let prevWasDash = false;
    for (let i = 0; i < length; i++) {
        const isFirst = i === 0;
        const isLast = i === length - 1;
        const charCount = isFirst || isLast ? 36 : 37;
        const charIdx = remaining % charCount;
        remaining = Math.floor(remaining / charCount);
        const isDash = charIdx === 36;
        if (isDash && prevWasDash) {
            return null;
        } // Invalid: consecutive dashes
        prevWasDash = isDash;
        result += CHARS_WITH_DASH[charIdx];
    }
    return result;
}
/**
 * Derive 128-bit key from room name using SHA256.
 * Room names are prefixed with '#' before hashing.
 */
export function deriveKeyFromRoomName(roomName) {
    if (roomName === PUBLIC_ROOM_NAME) {
        return PUBLIC_KEY;
    }
    const hash = SHA256(roomName);
    return hash.toString(Hex).substring(0, 32);
}
/**
 * Compute channel hash (first byte of SHA256(key)).
 */
export function getChannelHash(keyHex) {
    const hash = SHA256(Hex.parse(keyHex));
    return hash.toString(Hex).substring(0, 2);
}
/**
 * Verify MAC using HMAC-SHA256 with 32-byte padded key.
 */
export function verifyMac(ciphertext, cipherMac, keyHex) {
    const paddedKey = keyHex.padEnd(64, '0');
    const hmac = HmacSHA256(Hex.parse(ciphertext), Hex.parse(paddedKey));
    const computed = hmac.toString(Hex).substring(0, 4).toLowerCase();
    return computed === cipherMac.toLowerCase();
}
/**
 * Total index space for a given length (including invalid consecutive-dash indices).
 * This is the full mixed-radix space: 36 * 37^(len-2) * 36 for len >= 3.
 * Use this as the brute-force iteration bound (indexToRoomName returns null for holes).
 */
export function indexSpaceForLength(len) {
    if (len <= 0)
        return 0;
    if (len === 1)
        return CHARS_LEN;
    if (len === 2)
        return CHARS_LEN * CHARS_LEN;
    return CHARS_LEN * CHARS_LEN * Math.pow(CHARS_LEN + 1, len - 2);
}
/**
 * Count valid room names for a given length.
 * Accounts for dash rules (no start/end dash, no consecutive dashes).
 */
export function countNamesForLength(len) {
    if (len === 1) {
        return CHARS_LEN;
    }
    if (len === 2) {
        return CHARS_LEN * CHARS_LEN;
    }
    // For length >= 3: first and last are CHARS (36), middle follows no-consecutive-dash rule
    // Middle length = len - 2
    // Use DP: count sequences of length k with no consecutive dashes
    // endsWithNonDash[k], endsWithDash[k]
    let endsNonDash = CHARS_LEN; // length 1 middle
    let endsDash = 1;
    for (let i = 2; i <= len - 2; i++) {
        const newEndsNonDash = (endsNonDash + endsDash) * CHARS_LEN;
        const newEndsDash = endsNonDash; // dash can only follow non-dash
        endsNonDash = newEndsNonDash;
        endsDash = newEndsDash;
    }
    const middleCount = len > 2 ? endsNonDash + endsDash : 1;
    return CHARS_LEN * middleCount * CHARS_LEN;
}
/**
 * Check if timestamp is within the validity window.
 * @param timestamp - Unix timestamp to validate
 * @param validSeconds - Validity window in seconds (default: 30 days)
 * @param now - Current time for testing (default: current time)
 */
export function isTimestampValid(timestamp, validSeconds = DEFAULT_VALID_SECONDS, now) {
    const currentTime = now ?? Math.floor(Date.now() / 1000);
    return timestamp <= currentTime && timestamp >= currentTime - validSeconds;
}
/**
 * Check for valid UTF-8 (no replacement characters).
 */
export function isValidUtf8(text) {
    return !text.includes('\uFFFD');
}
/**
 * Check if text contains a colon character.
 */
export function hasColon(text) {
    return text.includes(':');
}
/**
 * Room name generator - iterates through all valid room names.
 */
export class RoomNameGenerator {
    constructor() {
        this.length = 1;
        this.indices = [0];
        this.done = false;
        this.currentInLength = 0;
        this.totalForLength = CHARS_LEN;
    }
    current() {
        return this.indices.map((i) => (i === CHARS_LEN ? '-' : CHARS[i])).join('');
    }
    getLength() {
        return this.length;
    }
    getCurrentInLength() {
        return this.currentInLength;
    }
    getTotalForLength() {
        return this.totalForLength;
    }
    getRemainingInLength() {
        return this.totalForLength - this.currentInLength;
    }
    isDone() {
        return this.done;
    }
    next() {
        if (this.done) {
            return false;
        }
        this.currentInLength++;
        // Increment with carry, respecting dash rules
        let pos = this.length - 1;
        while (pos >= 0) {
            const isFirst = pos === 0;
            const isLast = pos === this.length - 1;
            const maxVal = isFirst || isLast ? CHARS_LEN - 1 : CHARS_LEN; // CHARS_LEN = dash index
            if (this.indices[pos] < maxVal) {
                this.indices[pos]++;
                // Check dash rule: no consecutive dashes
                if (this.indices[pos] === CHARS_LEN && pos > 0 && this.indices[pos - 1] === CHARS_LEN) {
                    // Would create consecutive dashes, continue incrementing
                    continue;
                }
                // Reset all positions after this one
                for (let i = pos + 1; i < this.length; i++) {
                    this.indices[i] = 0;
                }
                // Validate: check no consecutive dashes in reset portion
                if (this.isValid()) {
                    return true;
                }
                continue;
            }
            pos--;
        }
        // Overflow - increase length
        this.length++;
        this.indices = new Array(this.length).fill(0);
        this.currentInLength = 0;
        this.totalForLength = countNamesForLength(this.length);
        return true;
    }
    isValid() {
        for (let i = 0; i < this.length; i++) {
            const isDash = this.indices[i] === CHARS_LEN;
            if (isDash && (i === 0 || i === this.length - 1)) {
                return false;
            }
            if (isDash && i > 0 && this.indices[i - 1] === CHARS_LEN) {
                return false;
            }
        }
        return true;
    }
    // Skip invalid combinations efficiently
    nextValid() {
        do {
            if (!this.next()) {
                return false;
            }
        } while (!this.isValid());
        return true;
    }
    // Skip to a specific (length, index) position
    // Index encoding: first char is LSB (consistent with indexToRoomName)
    skipTo(targetLength, targetIndex) {
        this.length = targetLength;
        this.indices = new Array(targetLength).fill(0);
        this.totalForLength = countNamesForLength(targetLength);
        // Convert index to indices array (LSB first = position 0)
        let remaining = targetIndex;
        for (let i = 0; i < targetLength; i++) {
            const isFirst = i === 0;
            const isLast = i === targetLength - 1;
            const charCount = isFirst || isLast ? CHARS_LEN : CHARS_LEN + 1;
            this.indices[i] = remaining % charCount;
            remaining = Math.floor(remaining / charCount);
        }
        this.currentInLength = targetIndex;
    }
}
//# sourceMappingURL=core.js.map