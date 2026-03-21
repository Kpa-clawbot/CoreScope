// WebGPU-accelerated word pair cracking for MeshCore packets
export class GpuWordPairs {
    constructor() {
        this.device = null;
        this.pipeline = null;
        this.bindGroupLayout = null;
        // Buffers
        this.paramsBuffer = null;
        this.matchCountBuffer = null;
        this.matchIBuffer = null; // i indices for matches
        this.matchJBuffer = null; // j indices for matches
        this.ciphertextBuffer = null;
        this.wordDataBuffer = null; // Packed word bytes
        this.wordOffsetsBuffer = null; // (offset, length) for each word
        // Staging buffers
        this.matchCountReadBuffer = null;
        this.matchIReadBuffer = null;
        this.matchJReadBuffer = null;
        // Cached state
        this.wordCount = 0;
        this.ciphertextBufferSize = 0;
        // Shader for word pair checking - optimized based on OpenCL reference implementation
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

// Pre-computed HMAC ipad/opad XOR states for common key padding (0x36/0x5c repeated)
const IPAD_XOR: u32 = 0x36363636u;
const OPAD_XOR: u32 = 0x5c5c5c5cu;

struct Params {
  target_channel_hash: u32,
  word_count: u32,
  i_start: u32,           // Starting i index (row) - computed on CPU from 64-bit offset
  j_start: u32,           // Starting j index (col) - computed on CPU from 64-bit offset
  batch_size: u32,
  target_mac: u32,
  ciphertext_words: u32,
  ciphertext_len_bits: u32,
  max_combined_len: u32,
  _padding: u32,
}

@group(0) @binding(0) var<uniform> params: Params;
@group(0) @binding(1) var<storage, read_write> match_count: atomic<u32>;
@group(0) @binding(2) var<storage, read_write> match_i: array<u32>;  // Separate array for i indices
@group(0) @binding(3) var<storage, read_write> match_j: array<u32>;  // Separate array for j indices
@group(0) @binding(4) var<storage, read> ciphertext: array<u32>;
@group(0) @binding(5) var<storage, read> word_data: array<u32>;
@group(0) @binding(6) var<storage, read> word_offsets: array<u32>;

// Inline bit rotation for better performance
fn rotr(x: u32, n: u32) -> u32 {
  return (x >> n) | (x << (32u - n));
}

// SHA256 compression function - processes one 64-byte block
// Takes mutable state h and message block msg
fn sha256_compress(h: ptr<function, array<u32, 8>>, msg: ptr<function, array<u32, 16>>) {
  var w: array<u32, 64>;

  // Load message into first 16 words
  w[0] = (*msg)[0]; w[1] = (*msg)[1]; w[2] = (*msg)[2]; w[3] = (*msg)[3];
  w[4] = (*msg)[4]; w[5] = (*msg)[5]; w[6] = (*msg)[6]; w[7] = (*msg)[7];
  w[8] = (*msg)[8]; w[9] = (*msg)[9]; w[10] = (*msg)[10]; w[11] = (*msg)[11];
  w[12] = (*msg)[12]; w[13] = (*msg)[13]; w[14] = (*msg)[14]; w[15] = (*msg)[15];

  // Extend message schedule
  for (var i = 16u; i < 64u; i++) {
    let s0 = rotr(w[i-15u], 7u) ^ rotr(w[i-15u], 18u) ^ (w[i-15u] >> 3u);
    let s1 = rotr(w[i-2u], 17u) ^ rotr(w[i-2u], 19u) ^ (w[i-2u] >> 10u);
    w[i] = w[i-16u] + s0 + w[i-7u] + s1;
  }

  var a = (*h)[0]; var b = (*h)[1]; var c = (*h)[2]; var d = (*h)[3];
  var e = (*h)[4]; var f = (*h)[5]; var g = (*h)[6]; var hv = (*h)[7];

  // Main compression loop
  for (var i = 0u; i < 64u; i++) {
    let S1 = rotr(e, 6u) ^ rotr(e, 11u) ^ rotr(e, 25u);
    let ch = (e & f) ^ (~e & g);
    let t1 = hv + S1 + ch + K[i] + w[i];
    let S0 = rotr(a, 2u) ^ rotr(a, 13u) ^ rotr(a, 22u);
    let maj = (a & b) ^ (a & c) ^ (b & c);
    let t2 = S0 + maj;
    hv = g; g = f; f = e; e = d + t1;
    d = c; c = b; b = a; a = t1 + t2;
  }

  (*h)[0] += a; (*h)[1] += b; (*h)[2] += c; (*h)[3] += d;
  (*h)[4] += e; (*h)[5] += f; (*h)[6] += g; (*h)[7] += hv;
}

// Initialize SHA256 state
fn sha256_init() -> array<u32, 8> {
  return array<u32, 8>(
    0x6a09e667u, 0xbb67ae85u, 0x3c6ef372u, 0xa54ff53au,
    0x510e527fu, 0x9b05688cu, 0x1f83d9abu, 0x5be0cd19u
  );
}

// Compute channel hash from 16-byte key
fn compute_channel_hash(key: array<u32, 4>) -> u32 {
  var h = sha256_init();
  var msg: array<u32, 16>;
  msg[0] = key[0]; msg[1] = key[1]; msg[2] = key[2]; msg[3] = key[3];
  msg[4] = 0x80000000u;
  msg[5] = 0u; msg[6] = 0u; msg[7] = 0u; msg[8] = 0u; msg[9] = 0u;
  msg[10] = 0u; msg[11] = 0u; msg[12] = 0u; msg[13] = 0u; msg[14] = 0u;
  msg[15] = 128u;
  sha256_compress(&h, &msg);
  return h[0] >> 24u;
}

// HMAC-SHA256 with precomputed ipad/opad states (optimization from OpenCL version)
// Returns first 2 bytes of HMAC as u32 (in high 16 bits)
fn hmac_sha256_optimized(key: array<u32, 4>) -> u32 {
  // Precompute ipad state: SHA256 state after processing (key XOR ipad)
  var h_ipad = sha256_init();
  var ipad_block: array<u32, 16>;
  ipad_block[0] = key[0] ^ IPAD_XOR;
  ipad_block[1] = key[1] ^ IPAD_XOR;
  ipad_block[2] = key[2] ^ IPAD_XOR;
  ipad_block[3] = key[3] ^ IPAD_XOR;
  ipad_block[4] = IPAD_XOR; ipad_block[5] = IPAD_XOR; ipad_block[6] = IPAD_XOR; ipad_block[7] = IPAD_XOR;
  ipad_block[8] = IPAD_XOR; ipad_block[9] = IPAD_XOR; ipad_block[10] = IPAD_XOR; ipad_block[11] = IPAD_XOR;
  ipad_block[12] = IPAD_XOR; ipad_block[13] = IPAD_XOR; ipad_block[14] = IPAD_XOR; ipad_block[15] = IPAD_XOR;
  sha256_compress(&h_ipad, &ipad_block);

  // Process ciphertext with ipad state
  var h = h_ipad;
  let ct_words = params.ciphertext_words;
  var word_idx = 0u;

  // Process full blocks
  while (word_idx + 16u <= ct_words) {
    var block: array<u32, 16>;
    block[0] = ciphertext[word_idx]; block[1] = ciphertext[word_idx+1u];
    block[2] = ciphertext[word_idx+2u]; block[3] = ciphertext[word_idx+3u];
    block[4] = ciphertext[word_idx+4u]; block[5] = ciphertext[word_idx+5u];
    block[6] = ciphertext[word_idx+6u]; block[7] = ciphertext[word_idx+7u];
    block[8] = ciphertext[word_idx+8u]; block[9] = ciphertext[word_idx+9u];
    block[10] = ciphertext[word_idx+10u]; block[11] = ciphertext[word_idx+11u];
    block[12] = ciphertext[word_idx+12u]; block[13] = ciphertext[word_idx+13u];
    block[14] = ciphertext[word_idx+14u]; block[15] = ciphertext[word_idx+15u];
    sha256_compress(&h, &block);
    word_idx += 16u;
  }

  // Final block with remaining ciphertext + padding
  var final_block: array<u32, 16>;
  let remaining = ct_words - word_idx;
  for (var i = 0u; i < 16u; i++) {
    if (i < remaining) {
      final_block[i] = ciphertext[word_idx + i];
    } else if (i == remaining) {
      final_block[i] = 0x80000000u;
    } else {
      final_block[i] = 0u;
    }
  }

  let total_bits = 512u + params.ciphertext_len_bits;
  if (remaining < 14u) {
    final_block[14] = 0u;
    final_block[15] = total_bits;
    sha256_compress(&h, &final_block);
  } else {
    sha256_compress(&h, &final_block);
    var len_block: array<u32, 16>;
    for (var i = 0u; i < 14u; i++) { len_block[i] = 0u; }
    len_block[14] = 0u;
    len_block[15] = total_bits;
    sha256_compress(&h, &len_block);
  }

  // Inner hash complete, now outer hash
  // Precompute opad state
  var h_opad = sha256_init();
  var opad_block: array<u32, 16>;
  opad_block[0] = key[0] ^ OPAD_XOR;
  opad_block[1] = key[1] ^ OPAD_XOR;
  opad_block[2] = key[2] ^ OPAD_XOR;
  opad_block[3] = key[3] ^ OPAD_XOR;
  opad_block[4] = OPAD_XOR; opad_block[5] = OPAD_XOR; opad_block[6] = OPAD_XOR; opad_block[7] = OPAD_XOR;
  opad_block[8] = OPAD_XOR; opad_block[9] = OPAD_XOR; opad_block[10] = OPAD_XOR; opad_block[11] = OPAD_XOR;
  opad_block[12] = OPAD_XOR; opad_block[13] = OPAD_XOR; opad_block[14] = OPAD_XOR; opad_block[15] = OPAD_XOR;
  sha256_compress(&h_opad, &opad_block);

  // Final HMAC block: inner_hash + padding
  var hash_block: array<u32, 16>;
  hash_block[0] = h[0]; hash_block[1] = h[1]; hash_block[2] = h[2]; hash_block[3] = h[3];
  hash_block[4] = h[4]; hash_block[5] = h[5]; hash_block[6] = h[6]; hash_block[7] = h[7];
  hash_block[8] = 0x80000000u;
  hash_block[9] = 0u; hash_block[10] = 0u; hash_block[11] = 0u;
  hash_block[12] = 0u; hash_block[13] = 0u; hash_block[14] = 0u;
  hash_block[15] = 512u + 256u;
  sha256_compress(&h_opad, &hash_block);

  return h_opad[0] & 0xFFFF0000u;
}

// Read a byte from packed word data (big-endian within u32)
fn read_byte(byte_offset: u32) -> u32 {
  let word_idx = byte_offset >> 2u;
  let byte_in_word = byte_offset & 3u;
  let word = word_data[word_idx];
  return (word >> ((3u - byte_in_word) << 3u)) & 0xFFu;
}

// Process a single word pair
fn process_word_pair(word1_idx: u32, word2_idx: u32) -> bool {
  let offset_len1 = word_offsets[word1_idx];
  let offset1 = offset_len1 >> 8u;    // 24 bits for offset (up to 16M bytes)
  let len1 = offset_len1 & 0xFFu;     // 8 bits for length (up to 255 chars)

  let offset_len2 = word_offsets[word2_idx];
  let offset2 = offset_len2 >> 8u;
  let len2 = offset_len2 & 0xFFu;

  let combined_len = len1 + len2;
  if (combined_len > params.max_combined_len) {
    return false;
  }

  // Check for consecutive dashes at join point
  if (len1 > 0u && len2 > 0u) {
    let last1 = read_byte(offset1 + len1 - 1u);
    let first2 = read_byte(offset2);
    if (last1 == 0x2du && first2 == 0x2du) {
      return false;
    }
  }

  // Build message: "#" + word1 + word2
  var msg: array<u32, 16>;
  for (var i = 0u; i < 16u; i++) { msg[i] = 0u; }

  let total_len = 1u + combined_len;
  var byte_pos = 0u;
  var current_word = 0x23000000u;  // '#' at position 0
  byte_pos = 1u;

  // Copy word1
  for (var i = 0u; i < len1; i++) {
    let c = read_byte(offset1 + i);
    let shift = (3u - (byte_pos & 3u)) << 3u;
    current_word |= c << shift;
    byte_pos++;
    if ((byte_pos & 3u) == 0u) {
      msg[(byte_pos >> 2u) - 1u] = current_word;
      current_word = 0u;
    }
  }

  // Copy word2
  for (var i = 0u; i < len2; i++) {
    let c = read_byte(offset2 + i);
    let shift = (3u - (byte_pos & 3u)) << 3u;
    current_word |= c << shift;
    byte_pos++;
    if ((byte_pos & 3u) == 0u) {
      msg[(byte_pos >> 2u) - 1u] = current_word;
      current_word = 0u;
    }
  }

  // Add 0x80 padding
  let shift = (3u - (byte_pos & 3u)) << 3u;
  current_word |= 0x80u << shift;
  msg[(byte_pos) >> 2u] = current_word;
  msg[15] = total_len << 3u;

  // Compute key = SHA256("#" + word1 + word2)
  var h = sha256_init();
  sha256_compress(&h, &msg);

  let key = array<u32, 4>(h[0], h[1], h[2], h[3]);

  // Check channel hash first (fast rejection)
  if (compute_channel_hash(key) != params.target_channel_hash) {
    return false;
  }

  // Verify MAC (expensive, only if channel hash matches)
  return hmac_sha256_optimized(key) == params.target_mac;
}

// Process multiple pairs per thread for better throughput
const PAIRS_PER_THREAD: u32 = 32u;

@compute @workgroup_size(256)
fn main(@builtin(global_invocation_id) global_id: vec3<u32>) {
  let base_idx = global_id.x * PAIRS_PER_THREAD;
  let word_count = params.word_count;
  let batch_size = params.batch_size;

  // i_start and j_start are computed on CPU from 64-bit batch_offset
  // This avoids needing 64-bit math in WGSL
  let i_start = params.i_start;
  let j_start = params.j_start;

  for (var p = 0u; p < PAIRS_PER_THREAD; p++) {
    let offset = base_idx + p;
    if (offset >= batch_size) { return; }

    // Compute actual (i, j) from starting position + offset
    // offset = local_i * word_count + local_j where local_j < word_count
    let total_j = j_start + offset;
    let extra_i = total_j / word_count;
    let i = i_start + extra_i;
    let j = total_j % word_count;

    if (i >= word_count) { return; }

    if (process_word_pair(i, j)) {
      let idx = atomicAdd(&match_count, 1u);
      if (idx < 1024u) {
        match_i[idx] = i;
        match_j[idx] = j;
      }
    }
  }
}
`;
    }
    async init() {
        if (!navigator.gpu) {
            return false;
        }
        try {
            const adapter = await navigator.gpu.requestAdapter();
            if (!adapter) {
                return false;
            }
            this.device = await adapter.requestDevice();
            this.bindGroupLayout = this.device.createBindGroupLayout({
                entries: [
                    { binding: 0, visibility: GPUShaderStage.COMPUTE, buffer: { type: 'uniform' } },
                    { binding: 1, visibility: GPUShaderStage.COMPUTE, buffer: { type: 'storage' } },
                    { binding: 2, visibility: GPUShaderStage.COMPUTE, buffer: { type: 'storage' } },
                    { binding: 3, visibility: GPUShaderStage.COMPUTE, buffer: { type: 'storage' } },
                    { binding: 4, visibility: GPUShaderStage.COMPUTE, buffer: { type: 'read-only-storage' } },
                    { binding: 5, visibility: GPUShaderStage.COMPUTE, buffer: { type: 'read-only-storage' } },
                    { binding: 6, visibility: GPUShaderStage.COMPUTE, buffer: { type: 'read-only-storage' } },
                ],
            });
            this.paramsBuffer = this.device.createBuffer({
                size: 40, // 10 x u32 for params struct with 64-bit offset
                usage: GPUBufferUsage.UNIFORM | GPUBufferUsage.COPY_DST,
            });
            this.matchCountBuffer = this.device.createBuffer({
                size: 4,
                usage: GPUBufferUsage.STORAGE | GPUBufferUsage.COPY_SRC | GPUBufferUsage.COPY_DST,
            });
            this.matchIBuffer = this.device.createBuffer({
                size: 1024 * 4,
                usage: GPUBufferUsage.STORAGE | GPUBufferUsage.COPY_SRC,
            });
            this.matchJBuffer = this.device.createBuffer({
                size: 1024 * 4,
                usage: GPUBufferUsage.STORAGE | GPUBufferUsage.COPY_SRC,
            });
            this.matchCountReadBuffer = this.device.createBuffer({
                size: 4,
                usage: GPUBufferUsage.MAP_READ | GPUBufferUsage.COPY_DST,
            });
            this.matchIReadBuffer = this.device.createBuffer({
                size: 1024 * 4,
                usage: GPUBufferUsage.MAP_READ | GPUBufferUsage.COPY_DST,
            });
            this.matchJReadBuffer = this.device.createBuffer({
                size: 1024 * 4,
                usage: GPUBufferUsage.MAP_READ | GPUBufferUsage.COPY_DST,
            });
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
            console.error('WebGPU word pairs initialization failed:', e);
            return false;
        }
    }
    /**
     * Upload the word list to GPU buffers.
     * Words are packed into a byte buffer, with offsets stored separately.
     */
    uploadWords(words) {
        if (!this.device) {
            throw new Error('GPU not initialized');
        }
        this.wordCount = words.length;
        // Calculate total byte size needed
        let totalBytes = 0;
        for (const word of words) {
            totalBytes += word.length;
        }
        // Pack words into byte array (big-endian word order for GPU)
        const wordData = new Uint8Array(Math.ceil(totalBytes / 4) * 4);
        const wordOffsets = new Uint32Array(words.length);
        let byteOffset = 0;
        for (let i = 0; i < words.length; i++) {
            const word = words[i];
            // Pack: 24 bits for offset (up to 16M), 8 bits for length (up to 255)
            wordOffsets[i] = (byteOffset << 8) | word.length;
            for (let j = 0; j < word.length; j++) {
                wordData[byteOffset++] = word.charCodeAt(j);
            }
        }
        // Convert to big-endian u32 for GPU
        const wordDataU32 = new Uint32Array(Math.ceil(totalBytes / 4));
        for (let i = 0; i < wordDataU32.length; i++) {
            wordDataU32[i] =
                (wordData[i * 4] << 24) |
                    (wordData[i * 4 + 1] << 16) |
                    (wordData[i * 4 + 2] << 8) |
                    wordData[i * 4 + 3];
        }
        // Create/recreate buffers
        if (this.wordDataBuffer) {
            this.wordDataBuffer.destroy();
        }
        if (this.wordOffsetsBuffer) {
            this.wordOffsetsBuffer.destroy();
        }
        this.wordDataBuffer = this.device.createBuffer({
            size: Math.max(wordDataU32.byteLength, 4),
            usage: GPUBufferUsage.STORAGE | GPUBufferUsage.COPY_DST,
        });
        this.device.queue.writeBuffer(this.wordDataBuffer, 0, wordDataU32);
        this.wordOffsetsBuffer = this.device.createBuffer({
            size: wordOffsets.byteLength,
            usage: GPUBufferUsage.STORAGE | GPUBufferUsage.COPY_DST,
        });
        this.device.queue.writeBuffer(this.wordOffsetsBuffer, 0, wordOffsets);
    }
    /**
     * Run a batch of word pair checks on the GPU.
     * @param targetChannelHash - Target channel hash byte
     * @param batchOffset - Starting pair index (i * wordCount + j)
     * @param batchSize - Number of pairs to check
     * @param ciphertextHex - Ciphertext for MAC verification
     * @param targetMacHex - Target MAC
     * @returns Array of matching pair indices as [i, j] tuples
     */
    async runBatch(targetChannelHash, batchOffset, batchSize, ciphertextHex, targetMacHex) {
        if (!this.device ||
            !this.pipeline ||
            !this.bindGroupLayout ||
            !this.paramsBuffer ||
            !this.matchCountBuffer ||
            !this.matchIBuffer ||
            !this.matchJBuffer ||
            !this.matchCountReadBuffer ||
            !this.matchIReadBuffer ||
            !this.matchJReadBuffer ||
            !this.wordDataBuffer ||
            !this.wordOffsetsBuffer) {
            throw new Error('GPU not initialized or words not uploaded');
        }
        // Parse ciphertext
        const ciphertextBytes = new Uint8Array(ciphertextHex.length / 2);
        for (let i = 0; i < ciphertextBytes.length; i++) {
            ciphertextBytes[i] = parseInt(ciphertextHex.substr(i * 2, 2), 16);
        }
        const ciphertextLenBits = ciphertextBytes.length * 8;
        const paddedLen = Math.ceil(ciphertextBytes.length / 4) * 4;
        const padded = new Uint8Array(paddedLen);
        padded.set(ciphertextBytes);
        const ciphertextWords = new Uint32Array(paddedLen / 4);
        for (let i = 0; i < ciphertextWords.length; i++) {
            ciphertextWords[i] =
                (padded[i * 4] << 24) |
                    (padded[i * 4 + 1] << 16) |
                    (padded[i * 4 + 2] << 8) |
                    padded[i * 4 + 3];
        }
        // Parse target MAC
        const macByte0 = parseInt(targetMacHex.substr(0, 2), 16);
        const macByte1 = parseInt(targetMacHex.substr(2, 2), 16);
        const targetMac = (macByte0 << 24) | (macByte1 << 16);
        // Resize ciphertext buffer if needed
        const requiredSize = Math.max(ciphertextWords.length * 4, 4);
        if (!this.ciphertextBuffer || this.ciphertextBufferSize < requiredSize) {
            if (this.ciphertextBuffer) {
                this.ciphertextBuffer.destroy();
            }
            this.ciphertextBuffer = this.device.createBuffer({
                size: requiredSize,
                usage: GPUBufferUsage.STORAGE | GPUBufferUsage.COPY_DST,
            });
            this.ciphertextBufferSize = requiredSize;
        }
        // Compute i_start and j_start from 64-bit batchOffset on CPU
        // This avoids needing 64-bit math in WGSL shader
        const iStart = Math.floor(batchOffset / this.wordCount);
        const jStart = batchOffset % this.wordCount;
        const paramsData = new Uint32Array([
            targetChannelHash,
            this.wordCount,
            iStart,
            jStart,
            batchSize,
            targetMac,
            ciphertextWords.length,
            ciphertextLenBits,
            30, // max combined length
            0, // padding
        ]);
        this.device.queue.writeBuffer(this.paramsBuffer, 0, paramsData);
        this.device.queue.writeBuffer(this.ciphertextBuffer, 0, ciphertextWords);
        this.device.queue.writeBuffer(this.matchCountBuffer, 0, GpuWordPairs.ZERO_DATA);
        // Create bind group
        const bindGroup = this.device.createBindGroup({
            layout: this.bindGroupLayout,
            entries: [
                { binding: 0, resource: { buffer: this.paramsBuffer } },
                { binding: 1, resource: { buffer: this.matchCountBuffer } },
                { binding: 2, resource: { buffer: this.matchIBuffer } },
                { binding: 3, resource: { buffer: this.matchJBuffer } },
                { binding: 4, resource: { buffer: this.ciphertextBuffer } },
                { binding: 5, resource: { buffer: this.wordDataBuffer } },
                { binding: 6, resource: { buffer: this.wordOffsetsBuffer } },
            ],
        });
        // Dispatch
        const commandEncoder = this.device.createCommandEncoder();
        const passEncoder = commandEncoder.beginComputePass();
        passEncoder.setPipeline(this.pipeline);
        passEncoder.setBindGroup(0, bindGroup);
        const PAIRS_PER_THREAD = 32; // Must match shader constant
        passEncoder.dispatchWorkgroups(Math.ceil(batchSize / (256 * PAIRS_PER_THREAD)));
        passEncoder.end();
        commandEncoder.copyBufferToBuffer(this.matchCountBuffer, 0, this.matchCountReadBuffer, 0, 4);
        commandEncoder.copyBufferToBuffer(this.matchIBuffer, 0, this.matchIReadBuffer, 0, 1024 * 4);
        commandEncoder.copyBufferToBuffer(this.matchJBuffer, 0, this.matchJReadBuffer, 0, 1024 * 4);
        this.device.queue.submit([commandEncoder.finish()]);
        // Read results
        await this.matchCountReadBuffer.mapAsync(GPUMapMode.READ);
        const matchCount = new Uint32Array(this.matchCountReadBuffer.getMappedRange())[0];
        this.matchCountReadBuffer.unmap();
        const matches = [];
        if (matchCount > 0) {
            await this.matchIReadBuffer.mapAsync(GPUMapMode.READ);
            await this.matchJReadBuffer.mapAsync(GPUMapMode.READ);
            const iIndices = new Uint32Array(this.matchIReadBuffer.getMappedRange());
            const jIndices = new Uint32Array(this.matchJReadBuffer.getMappedRange());
            for (let k = 0; k < Math.min(matchCount, 1024); k++) {
                matches.push([iIndices[k], jIndices[k]]);
            }
            this.matchIReadBuffer.unmap();
            this.matchJReadBuffer.unmap();
        }
        return matches;
    }
    getWordCount() {
        return this.wordCount;
    }
    destroy() {
        this.paramsBuffer?.destroy();
        this.matchCountBuffer?.destroy();
        this.matchIBuffer?.destroy();
        this.matchJBuffer?.destroy();
        this.ciphertextBuffer?.destroy();
        this.wordDataBuffer?.destroy();
        this.wordOffsetsBuffer?.destroy();
        this.matchCountReadBuffer?.destroy();
        this.matchIReadBuffer?.destroy();
        this.matchJReadBuffer?.destroy();
        if (this.device) {
            this.device.destroy();
            this.device = null;
        }
        this.pipeline = null;
        this.bindGroupLayout = null;
    }
}
GpuWordPairs.ZERO_DATA = new Uint32Array([0]);
//# sourceMappingURL=gpu-wordpairs.js.map