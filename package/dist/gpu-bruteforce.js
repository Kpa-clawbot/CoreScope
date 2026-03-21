// WebGPU-accelerated brute force key cracking for MeshCore packets
import { indexToRoomName, countNamesForLength } from './core.js';
export class GpuBruteForce {
    constructor() {
        this.device = null;
        this.pipeline = null;
        this.bindGroupLayout = null;
        // Persistent buffers for reuse between batches
        this.paramsBuffer = null;
        this.matchCountBuffer = null;
        this.matchIndicesBuffer = null;
        this.ciphertextBuffer = null;
        this.ciphertextBufferSize = 0;
        // Double-buffered staging buffers for overlapping GPU/CPU work
        this.matchCountReadBuffers = [null, null];
        this.matchIndicesReadBuffers = [null, null];
        this.currentReadBufferIndex = 0;
        // Cached bind group (recreated only when ciphertext buffer changes)
        this.bindGroup = null;
        this.bindGroupDirty = true;
        // Shader for SHA256 computation
        this.shaderCode = `
// SHA256 round constants
const K: array<u32, 64> = array<u32, 64>(
  0x428a2f98u, 0x71374491u, 0xb5c0fbcfu, 0xe9b5dba5u, 0x3956c25bu, 0x59f111f1u, 0x923f82a4u, 0xab1c5ed5u,
  0xd807aa98u, 0x12835b01u, 0x243185beu, 0x550c7dc3u, 0x72be5d74u, 0x80deb1feu, 0x9bdc06a7u, 0xc19bf174u,
  0xe49b69c1u, 0xefbe4786u, 0x0fc19dc6u, 0x240ca1ccu, 0x2de92c6fu, 0x4a7484aau, 0x5cb0a9dcu, 0x76f988dau,
  0x983e5152u, 0xa831c66du, 0xb00327c8u, 0xbf597fc7u, 0xc6e00bf3u, 0xd5a79147u, 0x06ca6351u, 0x14292967u,
  0x27b70a85u, 0x2e1b2138u, 0x4d2c6dfcu, 0x53380d13u, 0x650a7354u, 0x766a0abbu, 0x81c2c92eu, 0x92722c85u,
  0xa2bfe8a1u, 0xa81a664bu, 0xc24b8b70u, 0xc76c51a3u, 0xd192e819u, 0xd6990624u, 0xf40e3585u, 0x106aa070u,
  0x19a4c116u, 0x1e376c08u, 0x2748774cu, 0x34b0bcb5u, 0x391c0cb3u, 0x4ed8aa4au, 0x5b9cca4fu, 0x682e6ff3u,
  0x748f82eeu, 0x78a5636fu, 0x84c87814u, 0x8cc70208u, 0x90befffau, 0xa4506cebu, 0xbef9a3f7u, 0xc67178f2u
);

// Character lookup table (a-z = 0-25, 0-9 = 26-35, dash = 36)
const CHARS: array<u32, 37> = array<u32, 37>(
  0x61u, 0x62u, 0x63u, 0x64u, 0x65u, 0x66u, 0x67u, 0x68u, 0x69u, 0x6au, // a-j
  0x6bu, 0x6cu, 0x6du, 0x6eu, 0x6fu, 0x70u, 0x71u, 0x72u, 0x73u, 0x74u, // k-t
  0x75u, 0x76u, 0x77u, 0x78u, 0x79u, 0x7au,                             // u-z
  0x30u, 0x31u, 0x32u, 0x33u, 0x34u, 0x35u, 0x36u, 0x37u, 0x38u, 0x39u, // 0-9
  0x2du                                                                  // dash
);

struct Params {
  target_channel_hash: u32,
  batch_offset: u32,
  name_length: u32,
  batch_size: u32,
  target_mac: u32,           // First 2 bytes of target MAC (in high 16 bits)
  ciphertext_words: u32,     // Number of 32-bit words in ciphertext
  ciphertext_len_bits: u32,  // Length of ciphertext in bits
  verify_mac: u32,           // 1 to verify MAC, 0 to skip
}

@group(0) @binding(0) var<uniform> params: Params;
@group(0) @binding(1) var<storage, read_write> match_count: atomic<u32>;
@group(0) @binding(2) var<storage, read_write> match_indices: array<u32>;
@group(0) @binding(3) var<storage, read> ciphertext: array<u32>; // Ciphertext data

fn rotr(x: u32, n: u32) -> u32 {
  return (x >> n) | (x << (32u - n));
}

fn ch(x: u32, y: u32, z: u32) -> u32 {
  return (x & y) ^ (~x & z);
}

fn maj(x: u32, y: u32, z: u32) -> u32 {
  return (x & y) ^ (x & z) ^ (y & z);
}

fn sigma0(x: u32) -> u32 {
  return rotr(x, 2u) ^ rotr(x, 13u) ^ rotr(x, 22u);
}

fn sigma1(x: u32) -> u32 {
  return rotr(x, 6u) ^ rotr(x, 11u) ^ rotr(x, 25u);
}

fn gamma0(x: u32) -> u32 {
  return rotr(x, 7u) ^ rotr(x, 18u) ^ (x >> 3u);
}

fn gamma1(x: u32) -> u32 {
  return rotr(x, 17u) ^ rotr(x, 19u) ^ (x >> 10u);
}

// Convert index to room name bytes, returns the hash as a u32 for the first byte check
fn index_to_room_name(idx: u32, length: u32, msg: ptr<function, array<u32, 16>>) -> bool {
  // Message starts with '#' (0x23)
  var byte_pos = 0u;
  var word_idx = 0u;
  var current_word = 0x23000000u; // '#' in big-endian position 0
  byte_pos = 1u;

  var remaining = idx;
  var prev_was_dash = false;

  // Generate room name from index
  for (var i = 0u; i < length; i++) {
    let char_count = select(37u, 36u, i == 0u || i == length - 1u); // no dash at start/end
    var char_idx = remaining % char_count;
    remaining = remaining / char_count;

    // Check for consecutive dashes (invalid)
    let is_dash = char_idx == 36u && i > 0u && i < length - 1u;
    if (is_dash && prev_was_dash) {
      return false; // Invalid: consecutive dashes
    }
    prev_was_dash = is_dash;

    // Map char index to actual character
    let c = CHARS[char_idx];

    // Pack byte into current word (big-endian)
    let shift = (3u - byte_pos % 4u) * 8u;
    if (byte_pos % 4u == 0u && byte_pos > 0u) {
      (*msg)[word_idx] = current_word;
      word_idx = word_idx + 1u;
      current_word = 0u;
    }
    current_word = current_word | (c << shift);
    byte_pos = byte_pos + 1u;
  }

  // Add padding: 0x80 followed by zeros, then length in bits
  let msg_len_bits = (length + 1u) * 8u; // +1 for '#'

  // Add 0x80 padding byte
  let shift = (3u - byte_pos % 4u) * 8u;
  if (byte_pos % 4u == 0u) {
    (*msg)[word_idx] = current_word;
    word_idx = word_idx + 1u;
    current_word = 0x80000000u;
  } else {
    current_word = current_word | (0x80u << shift);
  }
  byte_pos = byte_pos + 1u;

  // Store current word
  if (byte_pos % 4u == 0u || word_idx < 14u) {
    (*msg)[word_idx] = current_word;
    word_idx = word_idx + 1u;
  }

  // Zero-fill until word 14
  for (var i = word_idx; i < 14u; i++) {
    (*msg)[i] = 0u;
  }

  // Length in bits (64-bit, but we only use lower 32 bits for short messages)
  (*msg)[14u] = 0u;
  (*msg)[15u] = msg_len_bits;

  return true;
}

fn sha256_block(msg: ptr<function, array<u32, 16>>) -> array<u32, 8> {
  // Initialize hash values
  var h: array<u32, 8> = array<u32, 8>(
    0x6a09e667u, 0xbb67ae85u, 0x3c6ef372u, 0xa54ff53au,
    0x510e527fu, 0x9b05688cu, 0x1f83d9abu, 0x5be0cd19u
  );

  // Message schedule
  var w: array<u32, 64>;
  for (var i = 0u; i < 16u; i++) {
    w[i] = (*msg)[i];
  }
  for (var i = 16u; i < 64u; i++) {
    w[i] = gamma1(w[i-2u]) + w[i-7u] + gamma0(w[i-15u]) + w[i-16u];
  }

  // Compression
  var a = h[0]; var b = h[1]; var c = h[2]; var d = h[3];
  var e = h[4]; var f = h[5]; var g = h[6]; var hh = h[7];

  for (var i = 0u; i < 64u; i++) {
    let t1 = hh + sigma1(e) + ch(e, f, g) + K[i] + w[i];
    let t2 = sigma0(a) + maj(a, b, c);
    hh = g; g = f; f = e; e = d + t1;
    d = c; c = b; b = a; a = t1 + t2;
  }

  h[0] = h[0] + a; h[1] = h[1] + b; h[2] = h[2] + c; h[3] = h[3] + d;
  h[4] = h[4] + e; h[5] = h[5] + f; h[6] = h[6] + g; h[7] = h[7] + hh;

  return h;
}

// Compute SHA256 of the key (16 bytes) to get channel hash
fn sha256_key(key: array<u32, 4>) -> u32 {
  var msg: array<u32, 16>;

  // Key bytes (16 bytes = 4 words)
  msg[0] = key[0];
  msg[1] = key[1];
  msg[2] = key[2];
  msg[3] = key[3];

  // Padding: 0x80 followed by zeros
  msg[4] = 0x80000000u;
  for (var i = 5u; i < 14u; i++) {
    msg[i] = 0u;
  }

  // Length: 128 bits
  msg[14] = 0u;
  msg[15] = 128u;

  let hash = sha256_block(&msg);

  // Return first byte of hash (big-endian)
  return hash[0] >> 24u;
}

// HMAC-SHA256 for MAC verification
// Key is 16 bytes (4 words), padded to 32 bytes with zeros for MeshCore
// Returns first 2 bytes of HMAC (as u32 in high 16 bits)
fn hmac_sha256_mac(key: array<u32, 4>, ciphertext_len: u32) -> u32 {
  // HMAC: H((K' ^ opad) || H((K' ^ ipad) || message))
  // K' is 64 bytes (32 bytes key + 32 bytes zero padding for MeshCore, then padded to 64)
  // ipad = 0x36 repeated, opad = 0x5c repeated

  // Build padded key (64 bytes = 16 words)
  // MeshCore uses 32-byte secret: 16-byte key + 16 zero bytes
  var k_pad: array<u32, 16>;
  k_pad[0] = key[0];
  k_pad[1] = key[1];
  k_pad[2] = key[2];
  k_pad[3] = key[3];
  for (var i = 4u; i < 16u; i++) {
    k_pad[i] = 0u;
  }

  // Inner hash: SHA256((K' ^ ipad) || message)
  // First block: K' ^ ipad (64 bytes)
  var inner_block: array<u32, 16>;
  for (var i = 0u; i < 16u; i++) {
    inner_block[i] = k_pad[i] ^ 0x36363636u;
  }

  // Initialize hash state with first block
  var h: array<u32, 8> = sha256_block(&inner_block);

  // Process ciphertext blocks (continuing from h state)
  let ciphertext_words = params.ciphertext_words;
  var word_idx = 0u;

  // Process full 64-byte blocks of ciphertext
  while (word_idx + 16u <= ciphertext_words) {
    var block: array<u32, 16>;
    for (var i = 0u; i < 16u; i++) {
      block[i] = ciphertext[word_idx + i];
    }
    h = sha256_block_continue(&block, h);
    word_idx = word_idx + 16u;
  }

  // Final block with remaining ciphertext + padding
  var final_block: array<u32, 16>;
  var remaining = ciphertext_words - word_idx;
  for (var i = 0u; i < 16u; i++) {
    if (i < remaining) {
      final_block[i] = ciphertext[word_idx + i];
    } else if (i == remaining) {
      // Add 0x80 padding
      final_block[i] = 0x80000000u;
    } else {
      final_block[i] = 0u;
    }
  }

  // Add length (64 bytes of ipad + ciphertext length)
  let total_bits = 512u + params.ciphertext_len_bits;
  if (remaining < 14u) {
    final_block[14] = 0u;
    final_block[15] = total_bits;
    h = sha256_block_continue(&final_block, h);
  } else {
    // Need extra block for length
    h = sha256_block_continue(&final_block, h);
    var len_block: array<u32, 16>;
    for (var i = 0u; i < 14u; i++) {
      len_block[i] = 0u;
    }
    len_block[14] = 0u;
    len_block[15] = total_bits;
    h = sha256_block_continue(&len_block, h);
  }

  let inner_hash = h;

  // Outer hash: SHA256((K' ^ opad) || inner_hash)
  var outer_block: array<u32, 16>;
  for (var i = 0u; i < 16u; i++) {
    outer_block[i] = k_pad[i] ^ 0x5c5c5c5cu;
  }
  h = sha256_block(&outer_block);

  // Second block: inner_hash (32 bytes) + padding
  var hash_block: array<u32, 16>;
  for (var i = 0u; i < 8u; i++) {
    hash_block[i] = inner_hash[i];
  }
  hash_block[8] = 0x80000000u;
  for (var i = 9u; i < 14u; i++) {
    hash_block[i] = 0u;
  }
  hash_block[14] = 0u;
  hash_block[15] = 512u + 256u; // 64 bytes opad + 32 bytes inner hash

  h = sha256_block_continue(&hash_block, h);

  // Return first 2 bytes (high 16 bits of first word)
  return h[0] & 0xFFFF0000u;
}

// SHA256 block computation continuing from existing state
fn sha256_block_continue(msg: ptr<function, array<u32, 16>>, h_in: array<u32, 8>) -> array<u32, 8> {
  var h = h_in;

  // Message schedule
  var w: array<u32, 64>;
  for (var i = 0u; i < 16u; i++) {
    w[i] = (*msg)[i];
  }
  for (var i = 16u; i < 64u; i++) {
    w[i] = gamma1(w[i-2u]) + w[i-7u] + gamma0(w[i-15u]) + w[i-16u];
  }

  // Compression
  var a = h[0]; var b = h[1]; var c = h[2]; var d = h[3];
  var e = h[4]; var f = h[5]; var g = h[6]; var hh = h[7];

  for (var i = 0u; i < 64u; i++) {
    let t1 = hh + sigma1(e) + ch(e, f, g) + K[i] + w[i];
    let t2 = sigma0(a) + maj(a, b, c);
    hh = g; g = f; f = e; e = d + t1;
    d = c; c = b; b = a; a = t1 + t2;
  }

  h[0] = h[0] + a; h[1] = h[1] + b; h[2] = h[2] + c; h[3] = h[3] + d;
  h[4] = h[4] + e; h[5] = h[5] + f; h[6] = h[6] + g; h[7] = h[7] + hh;

  return h;
}

// Process a single candidate and record match if found
fn process_candidate(name_idx: u32) {
  // Generate message for this room name
  var msg: array<u32, 16>;
  let valid = index_to_room_name(name_idx, params.name_length, &msg);

  if (!valid) {
    return;
  }

  // Compute SHA256("#roomname") - this gives us the key
  let key_hash = sha256_block(&msg);

  // Take first 16 bytes (4 words) as the key
  var key: array<u32, 4>;
  key[0] = key_hash[0];
  key[1] = key_hash[1];
  key[2] = key_hash[2];
  key[3] = key_hash[3];

  // Compute SHA256(key) to get channel hash
  let channel_hash = sha256_key(key);

  // Check if channel hash matches target
  if (channel_hash != params.target_channel_hash) {
    return;
  }

  // Channel hash matches - verify MAC if enabled
  if (params.verify_mac == 1u) {
    let computed_mac = hmac_sha256_mac(key, params.ciphertext_len_bits);
    if (computed_mac != params.target_mac) {
      return;
    }
  }

  // Found a match - record the index
  let match_idx = atomicAdd(&match_count, 1u);
  if (match_idx < 1024u) { // Limit stored matches
    match_indices[match_idx] = name_idx;
  }
}

// Each thread processes 32 candidates to amortize thread overhead
const CANDIDATES_PER_THREAD: u32 = 32u;

@compute @workgroup_size(256)
fn main(@builtin(global_invocation_id) global_id: vec3<u32>) {
  let base_idx = global_id.x * CANDIDATES_PER_THREAD;

  for (var i = 0u; i < CANDIDATES_PER_THREAD; i++) {
    let idx = base_idx + i;
    if (idx >= params.batch_size) {
      return;
    }
    let name_idx = params.batch_offset + idx;
    process_candidate(name_idx);
  }
}
`;
    }
    async init() {
        if (!navigator.gpu) {
            console.warn('WebGPU not supported');
            return false;
        }
        try {
            const adapter = await navigator.gpu.requestAdapter();
            if (!adapter) {
                console.warn('No GPU adapter found');
                return false;
            }
            this.device = await adapter.requestDevice();
            // Create bind group layout
            this.bindGroupLayout = this.device.createBindGroupLayout({
                entries: [
                    { binding: 0, visibility: GPUShaderStage.COMPUTE, buffer: { type: 'uniform' } },
                    { binding: 1, visibility: GPUShaderStage.COMPUTE, buffer: { type: 'storage' } },
                    { binding: 2, visibility: GPUShaderStage.COMPUTE, buffer: { type: 'storage' } },
                    { binding: 3, visibility: GPUShaderStage.COMPUTE, buffer: { type: 'read-only-storage' } },
                ],
            });
            // Create persistent buffers
            this.paramsBuffer = this.device.createBuffer({
                size: 32, // 8 u32s
                usage: GPUBufferUsage.UNIFORM | GPUBufferUsage.COPY_DST,
            });
            this.matchCountBuffer = this.device.createBuffer({
                size: 4,
                usage: GPUBufferUsage.STORAGE | GPUBufferUsage.COPY_SRC | GPUBufferUsage.COPY_DST,
            });
            this.matchIndicesBuffer = this.device.createBuffer({
                size: 1024 * 4, // Max 1024 matches per batch
                usage: GPUBufferUsage.STORAGE | GPUBufferUsage.COPY_SRC,
            });
            // Double-buffered staging buffers
            for (let i = 0; i < 2; i++) {
                this.matchCountReadBuffers[i] = this.device.createBuffer({
                    size: 4,
                    usage: GPUBufferUsage.MAP_READ | GPUBufferUsage.COPY_DST,
                });
                this.matchIndicesReadBuffers[i] = this.device.createBuffer({
                    size: 1024 * 4,
                    usage: GPUBufferUsage.MAP_READ | GPUBufferUsage.COPY_DST,
                });
            }
            // Create pipeline
            const shaderModule = this.device.createShaderModule({
                code: this.shaderCode,
            });
            const pipelineLayout = this.device.createPipelineLayout({
                bindGroupLayouts: [this.bindGroupLayout],
            });
            this.pipeline = this.device.createComputePipeline({
                layout: pipelineLayout,
                compute: {
                    module: shaderModule,
                    entryPoint: 'main',
                },
            });
            return true;
        }
        catch (e) {
            console.error('WebGPU initialization failed:', e);
            return false;
        }
    }
    isAvailable() {
        return this.device !== null && this.pipeline !== null;
    }
    // Convert room name index to actual room name string (delegates to core)
    indexToRoomName(idx, length) {
        return indexToRoomName(length, idx);
    }
    // Count valid names for a given length (delegates to core)
    countNamesForLength(len) {
        return countNamesForLength(len);
    }
    async runBatch(targetChannelHash, nameLength, batchOffset, batchSize, ciphertextHex, targetMacHex) {
        if (!this.device ||
            !this.pipeline ||
            !this.bindGroupLayout ||
            !this.paramsBuffer ||
            !this.matchCountBuffer ||
            !this.matchIndicesBuffer ||
            !this.matchCountReadBuffers[0] ||
            !this.matchCountReadBuffers[1] ||
            !this.matchIndicesReadBuffers[0] ||
            !this.matchIndicesReadBuffers[1]) {
            throw new Error('GPU not initialized');
        }
        // Swap to alternate staging buffer set (double-buffering)
        const readBufferIdx = this.currentReadBufferIndex;
        this.currentReadBufferIndex = 1 - this.currentReadBufferIndex;
        const matchCountReadBuffer = this.matchCountReadBuffers[readBufferIdx];
        const matchIndicesReadBuffer = this.matchIndicesReadBuffers[readBufferIdx];
        // Parse ciphertext if provided
        const verifyMac = ciphertextHex && targetMacHex ? 1 : 0;
        let ciphertextWords;
        let ciphertextLenBits = 0;
        let targetMac = 0;
        if (verifyMac) {
            // Convert hex to bytes then to big-endian u32 words
            const ciphertextBytes = new Uint8Array(ciphertextHex.length / 2);
            for (let i = 0; i < ciphertextBytes.length; i++) {
                ciphertextBytes[i] = parseInt(ciphertextHex.substr(i * 2, 2), 16);
            }
            ciphertextLenBits = ciphertextBytes.length * 8;
            // Pad to 4-byte boundary and convert to big-endian u32
            const paddedLen = Math.ceil(ciphertextBytes.length / 4) * 4;
            const padded = new Uint8Array(paddedLen);
            padded.set(ciphertextBytes);
            ciphertextWords = new Uint32Array(paddedLen / 4);
            for (let i = 0; i < ciphertextWords.length; i++) {
                ciphertextWords[i] =
                    (padded[i * 4] << 24) |
                        (padded[i * 4 + 1] << 16) |
                        (padded[i * 4 + 2] << 8) |
                        padded[i * 4 + 3];
            }
            // Parse target MAC (2 bytes in high 16 bits)
            const macByte0 = parseInt(targetMacHex.substr(0, 2), 16);
            const macByte1 = parseInt(targetMacHex.substr(2, 2), 16);
            targetMac = (macByte0 << 24) | (macByte1 << 16);
        }
        else {
            ciphertextWords = new Uint32Array([0]); // Dummy
        }
        // Resize ciphertext buffer if needed (marks bind group as dirty)
        const requiredCiphertextSize = Math.max(ciphertextWords.length * 4, 4);
        if (!this.ciphertextBuffer || this.ciphertextBufferSize < requiredCiphertextSize) {
            if (this.ciphertextBuffer) {
                this.ciphertextBuffer.destroy();
            }
            this.ciphertextBuffer = this.device.createBuffer({
                size: requiredCiphertextSize,
                usage: GPUBufferUsage.STORAGE | GPUBufferUsage.COPY_DST,
            });
            this.ciphertextBufferSize = requiredCiphertextSize;
            this.bindGroupDirty = true;
        }
        // Write params
        const paramsData = new Uint32Array([
            targetChannelHash,
            batchOffset,
            nameLength,
            batchSize,
            targetMac,
            ciphertextWords.length,
            ciphertextLenBits,
            verifyMac,
        ]);
        this.device.queue.writeBuffer(this.paramsBuffer, 0, paramsData);
        // Write ciphertext
        this.device.queue.writeBuffer(this.ciphertextBuffer, 0, ciphertextWords);
        // Reset match count (reuse static zero buffer)
        this.device.queue.writeBuffer(this.matchCountBuffer, 0, GpuBruteForce.ZERO_DATA);
        // Recreate bind group only if needed
        if (this.bindGroupDirty || !this.bindGroup) {
            this.bindGroup = this.device.createBindGroup({
                layout: this.bindGroupLayout,
                entries: [
                    { binding: 0, resource: { buffer: this.paramsBuffer } },
                    { binding: 1, resource: { buffer: this.matchCountBuffer } },
                    { binding: 2, resource: { buffer: this.matchIndicesBuffer } },
                    { binding: 3, resource: { buffer: this.ciphertextBuffer } },
                ],
            });
            this.bindGroupDirty = false;
        }
        // Create command encoder
        const commandEncoder = this.device.createCommandEncoder();
        const passEncoder = commandEncoder.beginComputePass();
        passEncoder.setPipeline(this.pipeline);
        passEncoder.setBindGroup(0, this.bindGroup);
        // Each workgroup has 256 threads, each processing 32 candidates
        const CANDIDATES_PER_THREAD = 32;
        passEncoder.dispatchWorkgroups(Math.ceil(batchSize / (256 * CANDIDATES_PER_THREAD)));
        passEncoder.end();
        // Copy results to current staging buffers
        commandEncoder.copyBufferToBuffer(this.matchCountBuffer, 0, matchCountReadBuffer, 0, 4);
        commandEncoder.copyBufferToBuffer(this.matchIndicesBuffer, 0, matchIndicesReadBuffer, 0, 1024 * 4);
        // Submit
        this.device.queue.submit([commandEncoder.finish()]);
        // Read results from current staging buffers
        await matchCountReadBuffer.mapAsync(GPUMapMode.READ);
        const matchCount = new Uint32Array(matchCountReadBuffer.getMappedRange())[0];
        matchCountReadBuffer.unmap();
        const matches = [];
        if (matchCount > 0) {
            await matchIndicesReadBuffer.mapAsync(GPUMapMode.READ);
            const indices = new Uint32Array(matchIndicesReadBuffer.getMappedRange());
            for (let i = 0; i < Math.min(matchCount, 1024); i++) {
                matches.push(indices[i]);
            }
            matchIndicesReadBuffer.unmap();
        }
        return matches;
    }
    destroy() {
        // Clean up persistent buffers
        this.paramsBuffer?.destroy();
        this.matchCountBuffer?.destroy();
        this.matchIndicesBuffer?.destroy();
        this.ciphertextBuffer?.destroy();
        // Clean up double-buffered staging buffers
        this.matchCountReadBuffers[0]?.destroy();
        this.matchCountReadBuffers[1]?.destroy();
        this.matchIndicesReadBuffers[0]?.destroy();
        this.matchIndicesReadBuffers[1]?.destroy();
        this.paramsBuffer = null;
        this.matchCountBuffer = null;
        this.matchIndicesBuffer = null;
        this.ciphertextBuffer = null;
        this.ciphertextBufferSize = 0;
        this.matchCountReadBuffers = [null, null];
        this.matchIndicesReadBuffers = [null, null];
        this.currentReadBufferIndex = 0;
        this.bindGroup = null;
        this.bindGroupDirty = true;
        if (this.device) {
            this.device.destroy();
            this.device = null;
        }
        this.pipeline = null;
        this.bindGroupLayout = null;
    }
}
// Reusable zero buffer for resetting match count
GpuBruteForce.ZERO_DATA = new Uint32Array([0]);
/**
 * Check if WebGPU is supported in the current browser.
 */
export function isWebGpuSupported() {
    return typeof navigator !== 'undefined' && 'gpu' in navigator;
}
//# sourceMappingURL=gpu-bruteforce.js.map