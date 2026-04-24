// Simple offline version of noble-ed25519 for MeshCore key generation
// This is a minimal implementation that provides the essential Ed25519 functions
// Based on noble-ed25519 v2.3.0

// Curve parameters (using BigInt for all values)
const CURVE = {
  n: 0x1000000000000000000000000000000014def9dea2f79cd65812631a5cf5d3edn,
  P: 0x7fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffedn,
  a: 0x7fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffecn,
  d: 0x52036cee2b6ffe738cc740797779e89800700a4d4141d8ab75eb4dca135978a3n,
  h: 0x08n,
  Gx: 0x216936d3cd6e53fec0a4e231fdd6dc5c692cc7609525a7b2c9562d608f25d51an,
  Gy: 0x6666666666666666666666666666666666666666666666666666666666666658n,
  G: [0x216936d3cd6e53fec0a4e231fdd6dc5c692cc7609525a7b2c9562d608f25d51an, 0x6666666666666666666666666666666666666666666666666666666666666658n]
};

// Utility functions
function mod(a, b = CURVE.P) {
  const result = a % b;
  return result >= 0n ? result : b + result;
}

function pow2(x, power) {
  const { P } = CURVE;
  let res = x;
  while (power > 0n) {
    res = mod(res * res);
    power -= 1n;
  }
  return res;
}

function pow_2_252_3(x) {
  const P = CURVE.P;
  const x2 = mod(x * x);
  const x3 = mod(x2 * x);
  const x6 = pow2(x3, 3n);
  const x9 = mod(x6 * x3);
  const x11 = mod(x9 * x2);
  const x22 = pow2(x11, 11n);
  const x44 = pow2(x22, 22n);
  const x88 = pow2(x44, 44n);
  const x176 = pow2(x88, 88n);
  const x220 = mod(x176 * x44);
  const x223 = mod(x220 * x3);
  const t1 = pow2(x223, 23n);
  const t2 = mod(t1 * x22);
  const t3 = pow2(t2, 6n);
  const t4 = mod(t3 * x11);
  const t5 = pow2(t4, 2n);
  return mod(t5 * x);
}

function sqrt_ratio_3mod4(u, v) {
  const P = CURVE.P;
  const v3 = mod(v * v * v, P);
  const v7 = mod(v3 * v3 * v, P);
  const pow = pow_2_252_3(u * v7);
  let x = mod(u * v3 * pow);
  const vx2 = mod(v * x * x);
  const root1 = x;
  const root2 = mod(x * CURVE.Gx);
  const useRoot1 = vx2 === u;
  const useRoot2 = vx2 === mod(-u);
  const noRoot = vx2 === mod(-u * CURVE.Gx);
  if (useRoot1) x = root1;
  else if (useRoot2 || noRoot) x = root2;
  else throw new Error('Invalid point');
  const isNegative = (x & 1n) === 1n;
  if (isNegative) x = mod(-x);
  return x;
}

// Point operations
function pointAdd(p1, p2) {
  const [x1, y1] = p1;
  const [x2, y2] = p2;
  const { P, d } = CURVE;
  
  const x1y2 = mod(x1 * y2);
  const x2y1 = mod(x2 * y1);
  const dx1x2y1y2 = mod(d * x1 * x2 * y1 * y2);
  
  const x3 = mod((x1y2 + x2y1) * mod(1n + dx1x2y1y2));
  const y3 = mod((y1 * y2 + x1 * x2) * mod(1n - dx1x2y1y2));
  
  return [x3, y3];
}

function pointMultiply(point, scalar) {
  let result = [0n, 1n];
  let current = point;
  
  for (let i = 0; i < 256; i++) {
    if (scalar & (1n << BigInt(i))) {
      result = pointAdd(result, current);
    }
    current = pointAdd(current, current);
  }
  
  return result;
}

// Main functions
function getPublicKey(privateKey) {
  if (!(privateKey instanceof Uint8Array)) {
    throw new Error('Private key must be Uint8Array');
  }
  if (privateKey.length !== 32) {
    throw new Error('Private key must be 32 bytes');
  }
  
  // Convert to bigint
  let scalar = 0n;
  for (let i = 0; i < 32; i++) {
    scalar += BigInt(privateKey[i]) << BigInt(8 * i);
  }
  
  // Clamp the scalar
  scalar = mod(scalar, CURVE.n);
  scalar &= ~(7n << 252n);
  scalar |= (1n << 254n);
  
  // Multiply base point
  const point = pointMultiply(CURVE.G, scalar);
  
  // Convert to bytes
  const x = point[0];
  const y = point[1];
  const result = new Uint8Array(32);
  
  for (let i = 0; i < 32; i++) {
    result[i] = Number((y >> BigInt(8 * i)) & 255n);
  }
  result[31] |= Number((x & 1n) << 7);
  
  return result;
}

// Export the functions
export { getPublicKey };
