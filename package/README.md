![NPM Version](https://img.shields.io/npm/v/meshcore-hashtag-cracker)

# MeshCore GroupText Hashtag Room Cracker

Standalone library for cracking MeshCore GroupText packets from hashtag rooms using WebGPU-accelerated brute force (with fallbacks for our non-GPU brethren and dictionary attack support).

**Note:** This tool is designed exclusively for cracking public hashtag rooms (e.g., `#general`, `#test`). It does not support private rooms or other MeshCore encryption schemes (or, rather, it will attempt to crack them, but nearly certainly fail)

This is an LLM-developed library and has borne out its correctness in various application uses, but caution should still be applied in any mission-critical contexts.

## Features

- WebGPU-accelerated brute force (100M+ keys/second on modern GPUs)
- Dictionary attack support with built-in English wordlist (482k words)
- Configurable filters (sender, UTF-8, timestamp) to handle MAC collisions with sanity checks
- Progress callbacks with ETA
- Resume support for interrupted searches

## Installation

### npm

```bash
npm install meshcore-hashtag-cracker
```

### Browser (Direct Include)

For direct browser usage without a bundler, download [`browser/meshcore_cracker.min.js`](./browser/meshcore_cracker.min.js) and include it:

```html
<script src="meshcore_cracker.min.js"></script>
<script>
  const cracker = new MeshCoreCracker.GroupTextCracker();

  // Optional: load a wordlist for dictionary attack (tried before GPU brute force)
  // await cracker.loadWordlist('https://example.com/words.txt');

  cracker.crack('150013752F15A1BF3C018EB1FC4F26B5FAEB417BB0F1AE8FF07655484EBAA05CB9A927D689', {
    maxLength: 4
  }).then(result => {
    if (result.found) {
      console.log('Room:', result.roomName);
      console.log('Message:', result.decryptedMessage);
    }
    cracker.destroy();
  });
</script>
```

## Usage

```typescript
import { GroupTextCracker } from 'meshcore-hashtag-cracker';
// Built-in 482k word English dictionary (tree-shakeable, ~4MB)
// Dictionary is checked BEFORE GPU brute force - a room like #football
// takes hours to brute force but milliseconds via dictionary lookup
import { ENGLISH_WORDLIST } from 'meshcore-hashtag-cracker/wordlist';

const cracker = new GroupTextCracker();
cracker.setWordlist(ENGLISH_WORDLIST);

// Example GroupText packet (hex string, no spaces or 0x prefix)
const packetHex = '150013752F15A1BF3C018EB1FC4F26B5FAEB417BB0F1AE8FF07655484EBAA05CB9A927D689';

const result = await cracker.crack(packetHex, {
  maxLength: 6,
});

if (result.found) {
  console.log(`Room: #${result.roomName}`);
  console.log(`Key: ${result.key}`);
  console.log(`Message: ${result.decryptedMessage}`);
}

cracker.destroy();
```

**Output:**
```
Room: #aa
Key: e147f36926b7b509af9b41b65304dc30
Message: SenderName: Hello world!
```

Note: When a sender is detected in the message, `decryptedMessage` includes the full "sender: message" format.

### Options

```typescript
const result = await cracker.crack(packetHex, {
  maxLength: 8,           // Max room name length to try (default: 8)
  startingLength: 1,      // Min room name length to try (default: 1)
  useDictionary: true,    // Try dictionary words first (default: true); needs cracker.setWordlist() called first
  useSenderFilter: true,  // Reject messages without sender (default: true)
  useUtf8Filter: true,    // Reject invalid UTF-8 (default: true)
  useTimestampFilter: true, // Reject old timestamps (default: true)
  validSeconds: 2592000,  // Timestamp window in seconds (default: 30 days)
  forceCpu: false,        // Force CPU mode, skip GPU (default: false)
  startFrom: 'abc',       // Resume after this position (optional)
  startFromType: 'bruteforce', // 'dictionary', 'dictionary-pair', or 'bruteforce' (default: 'bruteforce')
});
```

For detailed API documentation, see [API.md](./API.md).

## Browser Requirements

- WebGPU support (Chrome 113+, Edge 113+, or other Chromium-based browsers)
- HTTPS connection for non-localhost hostnames (falls back gracefully with an error if WebGPU is not available)

## Performance

Typical performance on modern hardware:
- **GPU (RTX 3080)**: ~500M keys/second
- **GPU (integrated)**: ~50M keys/second

Search space by room name length:
| Length | Candidates | Time @ 100M/s |
|--------|------------|---------------|
| 1 | 36 | instant |
| 2 | 1,296 | instant |
| 3 | 47,952 | instant |
| 4 | 1,774,224 | <1s |
| 5 | 65,646,288 | <1s |
| 6 | 2,428,912,656 | ~24s |
| 7 | 89,869,768,272 | ~15min |
| 8 | 3,325,181,426,064 | ~9h |

## Development

```bash
# Install dependencies
npm install

# Build
npm run build

# Run tests
npm test

# Run tests in watch mode
npm run test:watch
```

## License

MIT

This library uses town and placenames in the word list sourced from https://simplemaps.com/data/us-cities, used under CC BY 4.0.

This library also uses airport codes sourced from https://en.wikipedia.org/wiki/List_of_airports_in_the_United_States.
