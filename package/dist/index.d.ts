/**
 * MeshCore Cracker - Standalone library for cracking MeshCore GroupText packets
 *
 * @example
 * ```typescript
 * import { GroupTextCracker } from 'meshcore-cracker';
 *
 * const cracker = new GroupTextCracker();
 *
 * // Optional: load wordlist for dictionary attack
 * await cracker.loadWordlist('/words.txt');
 *
 * const result = await cracker.crack(packetHex, {
 *   maxLength: 6,
 *   useSenderFilter: true,
 *   useUtf8Filter: true,
 * }, (progress) => {
 *   console.log(`${progress.percent.toFixed(1)}% - ETA: ${progress.etaSeconds}s`);
 * });
 *
 * if (result.found) {
 *   console.log(`Room: #${result.roomName}`);
 *   console.log(`Message: ${result.decryptedMessage}`);
 * }
 *
 * cracker.destroy();
 * ```
 */
export { GroupTextCracker } from './cracker.js';
export type { CrackOptions, CrackResult, ProgressReport, ProgressCallback, DecodedPacket, } from './types.js';
export { deriveKeyFromRoomName, getChannelHash, verifyMac, isTimestampValid, isValidUtf8, hasColon, indexToRoomName, roomNameToIndex, countNamesForLength, indexSpaceForLength, PUBLIC_ROOM_NAME, PUBLIC_KEY, DEFAULT_VALID_SECONDS, } from './core.js';
export { isWebGpuSupported } from './gpu-bruteforce.js';
//# sourceMappingURL=index.d.ts.map