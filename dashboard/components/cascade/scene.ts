/**
 * The Snapfall cascade: a code-only Three.js diorama of where customers' money falls.
 *
 * Built the img2threejs way (img2threejs/img2threejs, grimoire/build/geometry_patterns.md): procedural
 * geometry only, no mesh or texture files; lathe bowls for the basins, displaced boxes for the rock,
 * an instanced mesh for the droplets; stone as a mottled canvas texture with high roughness, water
 * with an environment reflection so it does not read as a flat sheet; every part a named mesh that
 * can be clicked.
 *
 * Three stone shelves step down the frame. Escrow sits at the top. Its water falls first into the
 * capital pool's basin, and only what the pool does not keep spills on into the operator's basin,
 * which is the order JobVault.acceptDelivery pays in. Stream widths, droplet counts and basin levels
 * come from the real settled amounts handed in; nothing here is decorative data.
 *
 * Client only: the caller imports this module from an effect, so three.js never reaches the server
 * bundle or the first paint.
 */
import * as THREE from 'three';
import { RoomEnvironment } from 'three/examples/jsm/environments/RoomEnvironment.js';
import { GLTFLoader } from 'three/examples/jsm/loaders/GLTFLoader.js';
import { MeshoptDecoder } from 'three/examples/jsm/libs/meshopt_decoder.module.js';

export type PartName = 'escrow' | 'pool' | 'operator';

export interface CascadeJob {
  id: string;
  paid: number;
  pool: number;
  op: number;
}

export interface CascadeData {
  paid: number;
  pool: number;
  op: number;
  jobs: CascadeJob[];
}

/** Screen positions (CSS px inside the container) of the three label anchors. */
export type Anchors = Record<PartName, { x: number; y: number; visible: boolean }>;

export type StageMode = 'day' | 'night';

export interface CascadeOptions {
  data: CascadeData;
  reduced: boolean;
  mode: StageMode;
  /** A sculpted fountain (GLB) to replace the procedural rock with; the rock stays if it fails. */
  modelUrl?: string;
  onFrame?: (a: Anchors) => void;
  onJob?: (index: number) => void;
  onPick?: (part: PartName | null) => void;
  onFail?: () => void;
}

export interface CascadeHandle {
  setData(d: CascadeData): void;
  setPaused(p: boolean): void;
  setReduced(r: boolean): void;
  setSelected(p: PartName | null): void;
  setMode(m: StageMode): void;
  showJob(index: number): void;
  dispose(): void;
}

// The stage follows the page theme: daylight on paper, night on ink. Pool and operator are the
// validated series pairs (light #2563EB / #C2650A, dark #3B82F6 / #D97706).
const PALETTE = {
  day: {
    pool: '#2563EB', op: '#C2650A', inflow: '#6F98C2', fog: '#E6EDF3', mist: '#ffffff',
    stone: '#f2efe8', stoneDark: '#d9d5cc', sky: '#dde9f3', ground: '#c9bba3',
    hemi: 1.6, key: 2.4, exposure: 1.05, fogNear: 14, fogFar: 30, additive: false,
  },
  night: {
    pool: '#3B82F6', op: '#D97706', inflow: '#D6E4F2', fog: '#090c12', mist: '#D6E4F2',
    stone: '#7d828b', stoneDark: '#585c64', sky: '#a9bfdc', ground: '#3a2b1d',
    hemi: 1.4, key: 2.6, exposure: 1.2, fogNear: 12, fogFar: 24, additive: true,
  },
} as const;
/**
 * Where the fountain model's parts are, in the model's own units (its bounding box is about
 * 1.9 x 0.96 x 1.0, the shelf high on the left, the pool's bowl in the middle, the operator's wide
 * bowl low on the right). Measured from the mesh and tuned against the render with ?cascadeDebug.
 */
const FOUNTAIN = {
  scale: 4.0,
  offset: new THREE.Vector3(0.05, 0.12, 0),
  rotationY: 0,
  shelfLip: new THREE.Vector3(-0.07, 0.43, 0.04),
  escrowWater: { x: -0.52, y: 0.468, z: 0.0, w: 0.7, d: 0.38 },
  pool: { x: -0.08, rim: 0.34, floor: 0.06, z: 0.0, r: 0.24, rz: 1 },
  poolLip: new THREE.Vector3(0.34, 0.08, 0.08),
  op: { x: 0.6, rim: -0.16, floor: -0.28, z: 0.0, r: 0.3, rz: 1 },
};

const POOL = new THREE.Color(PALETTE.night.pool);
const OP = new THREE.Color(PALETTE.night.op);
const INFLOW = new THREE.Color(PALETTE.night.inflow);
const FOG = new THREE.Color(PALETTE.night.fog);

const REPLAY_MS = 4200;
const GRAVITY = 9.8;

// ── deterministic noise: the rock must look the same on every load ──
function mulberry(seed: number) {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

function hash3(x: number, y: number, z: number): number {
  const s = Math.sin(x * 127.1 + y * 311.7 + z * 74.7) * 43758.5453;
  return s - Math.floor(s);
}

function valueNoise(x: number, y: number, z: number): number {
  const xi = Math.floor(x), yi = Math.floor(y), zi = Math.floor(z);
  const xf = x - xi, yf = y - yi, zf = z - zi;
  const u = xf * xf * (3 - 2 * xf), v = yf * yf * (3 - 2 * yf), w = zf * zf * (3 - 2 * zf);
  const lerp = (a: number, b: number, t: number) => a + (b - a) * t;
  const c = (dx: number, dy: number, dz: number) => hash3(xi + dx, yi + dy, zi + dz);
  return lerp(
    lerp(lerp(c(0, 0, 0), c(1, 0, 0), u), lerp(c(0, 1, 0), c(1, 1, 0), u), v),
    lerp(lerp(c(0, 0, 1), c(1, 0, 1), u), lerp(c(0, 1, 1), c(1, 1, 1), u), v),
    w,
  );
}

function fbm(x: number, y: number, z: number): number {
  return valueNoise(x, y, z) * 0.55 + valueNoise(x * 2.1, y * 2.1, z * 2.1) * 0.3 + valueNoise(x * 4.3, y * 4.3, z * 4.3) * 0.15;
}

/** Mottled stone albedo (geometry_patterns.md, "stone": mottled albedo, high roughness). */
function stoneTexture(seed: number): THREE.CanvasTexture {
  const size = 256;
  const c = document.createElement('canvas');
  c.width = c.height = size;
  const g = c.getContext('2d')!;
  const img = g.createImageData(size, size);
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      const n = fbm(x / 22 + seed, y / 22, seed * 0.3);
      const streak = valueNoise(x / 90, y / 7, seed) * 0.18; // faint bedding lines
      const v = 0.55 + (n - 0.5) * 0.7 + streak;
      const i = (y * size + x) * 4;
      // neutral warm grey: the material colour per theme does the tinting
      img.data[i] = Math.max(0, Math.min(255, 150 * v + 62));
      img.data[i + 1] = Math.max(0, Math.min(255, 146 * v + 60));
      img.data[i + 2] = Math.max(0, Math.min(255, 138 * v + 58));
      img.data[i + 3] = 255;
    }
  }
  g.putImageData(img, 0, 0);
  const t = new THREE.CanvasTexture(c);
  t.colorSpace = THREE.SRGBColorSpace;
  t.wrapS = t.wrapT = THREE.RepeatWrapping;
  return t;
}

/** A soft round sprite for mist. */
function mistTexture(): THREE.CanvasTexture {
  const c = document.createElement('canvas');
  c.width = c.height = 64;
  const g = c.getContext('2d')!;
  const grad = g.createRadialGradient(32, 32, 0, 32, 32, 32);
  grad.addColorStop(0, 'rgba(255,255,255,0.9)');
  grad.addColorStop(0.4, 'rgba(255,255,255,0.35)');
  grad.addColorStop(1, 'rgba(255,255,255,0)');
  g.fillStyle = grad;
  g.fillRect(0, 0, 64, 64);
  return new THREE.CanvasTexture(c);
}

/**
 * A rock shelf: a subdivided box whose sides are pushed in and out by noise and whose top stays
 * nearly flat, so it reads as a ledge water can run along (verification cue: no edge perfectly sharp
 * or perfectly smooth).
 */
function rockGeometry(w: number, h: number, d: number, seed: number): THREE.BufferGeometry {
  const geo = new THREE.BoxGeometry(w, h, d, Math.ceil(w * 7), Math.ceil(h * 7), Math.ceil(d * 7));
  const pos = geo.attributes.position as THREE.BufferAttribute;
  const p = new THREE.Vector3();
  for (let i = 0; i < pos.count; i++) {
    p.fromBufferAttribute(pos, i);
    const top = p.y > h / 2 - 1e-4;
    const amp = top ? 0.04 : 0.2;
    const n = fbm(p.x * 1.3 + seed, p.y * 1.3, p.z * 1.3 + seed) - 0.5;
    // Round the box: pull corners in so the silhouette is a stone, not a crate.
    const cx = Math.abs(p.x) / (w / 2), cz = Math.abs(p.z) / (d / 2);
    const corner = Math.max(0, cx * cz - 0.35) * 0.6;
    p.x *= 1 - corner * 0.5;
    p.z *= 1 - corner * 0.5;
    p.addScaledVector(p.clone().normalize(), n * amp * 2);
    // taper: the shelf's underside narrows, so it reads as an overhang
    const below = (h / 2 - p.y) / h;
    p.x *= 1 - below * 0.12;
    p.z *= 1 - below * 0.18;
    if (top) p.y = h / 2 + n * amp;
    pos.setXYZ(i, p.x, p.y, p.z);
  }
  geo.computeVertexNormals();
  return geo;
}

/**
 * A boulder: a subdivided icosahedron scaled to size and pushed out by noise, flattened a little on
 * top so a shelf can sit on it. Organic masses get an organic primitive; a box under a basin reads
 * as a crate (the first render's failure).
 */
function boulderGeometry(sx: number, sy: number, sz: number, seed: number): THREE.BufferGeometry {
  const geo = new THREE.IcosahedronGeometry(1, 5);
  const pos = geo.attributes.position as THREE.BufferAttribute;
  const p = new THREE.Vector3();
  for (let i = 0; i < pos.count; i++) {
    p.fromBufferAttribute(pos, i);
    const n = fbm(p.x * 1.6 + seed, p.y * 1.6 + seed * 0.5, p.z * 1.6) - 0.5;
    const ridge = Math.abs(valueNoise(p.x * 4 + seed, p.y * 4, p.z * 4) - 0.5) * 0.12;
    const r = 1 + n * 0.45 - ridge;
    p.multiplyScalar(r);
    if (p.y > 0.55) p.y = 0.55 + (p.y - 0.55) * 0.35; // a flattish crown
    pos.setXYZ(i, p.x * sx, p.y * sy, p.z * sz);
  }
  geo.computeVertexNormals();
  return geo;
}

/** A shallow carved basin: a lathe bowl with a lip, open at the top. */
function basinGeometry(radius: number, depth: number): THREE.LatheGeometry {
  const pts: THREE.Vector2[] = [];
  const steps = 14;
  for (let i = 0; i <= steps; i++) {
    const t = i / steps;
    // inner wall from the floor up to the lip
    pts.push(new THREE.Vector2(radius * (0.15 + 0.85 * Math.sin((t * Math.PI) / 2)), -depth + depth * t * t));
  }
  pts.push(new THREE.Vector2(radius * 1.12, 0.04)); // rounded lip
  pts.push(new THREE.Vector2(radius * 1.2, -0.08));
  pts.push(new THREE.Vector2(radius * 1.24, -depth * 1.6)); // outer flank
  return new THREE.LatheGeometry(pts, 48);
}

interface Stream {
  mesh: THREE.InstancedMesh;
  from: THREE.Vector3;
  to: THREE.Vector3;
  width: number;
  count: number;
  phase: Float32Array;
  lane: Float32Array;
  jitter: Float32Array;
  flight: number;
  vx: number;
  vy0: number;
}

export function mountCascade(container: HTMLElement, opts: CascadeOptions): CascadeHandle {
  let data = opts.data;
  let mode: StageMode = opts.mode;
  let reduced = opts.reduced;
  let paused = false;
  let selected: PartName | null = null;
  let hovered: PartName | null = null;
  let jobIndex = 0;
  let lastSwitch = performance.now();
  let surge = 0;
  let running = false;
  let visible = true;
  let raf = 0;
  let disposed = false;

  // ── renderer ──
  let renderer: THREE.WebGLRenderer;
  try {
    renderer = new THREE.WebGLRenderer({ antialias: true, alpha: true, powerPreference: 'low-power' });
  } catch {
    opts.onFail?.();
    return { setData() {}, setPaused() {}, setReduced() {}, setSelected() {}, setMode() {}, showJob() {}, dispose() {} };
  }
  renderer.setPixelRatio(Math.min(window.devicePixelRatio || 1, 1.75));
  renderer.outputColorSpace = THREE.SRGBColorSpace;
  renderer.toneMapping = THREE.ACESFilmicToneMapping;
  renderer.toneMappingExposure = 1.25;
  renderer.setClearColor(0x000000, 0);
  const canvas = renderer.domElement;
  canvas.className = 'cascade-canvas';
  canvas.setAttribute('aria-hidden', 'true');
  container.appendChild(canvas);

  const onLost = (e: Event) => {
    e.preventDefault();
    stop();
    opts.onFail?.();
  };
  canvas.addEventListener('webglcontextlost', onLost);

  const scene = new THREE.Scene();
  scene.fog = new THREE.Fog(FOG, 12, 24);
  const pmrem = new THREE.PMREMGenerator(renderer);
  const envTex = pmrem.fromScene(new RoomEnvironment(), 0.04).texture;
  scene.environment = envTex;
  scene.environmentIntensity = 0.35;

  const camera = new THREE.PerspectiveCamera(30, 1, 0.1, 60);
  const camBase = new THREE.Vector3(1.0, 4.6, 13.2);
  const look = new THREE.Vector3(0.3, 0.35, 0);
  camera.position.copy(camBase);

  // ── light: explains the form instead of hiding it ──
  const hemi = new THREE.HemisphereLight('#a9cbdc', '#3a2b1d', 1.5);
  scene.add(hemi);
  const key = new THREE.DirectionalLight('#ffe6c8', 2.8);
  key.position.set(-4, 9, 7);
  scene.add(key);
  const rim = new THREE.DirectionalLight('#7fb7d6', 1.1);
  rim.position.set(4, 3, -6);
  scene.add(rim);
  const poolGlow = new THREE.PointLight(POOL, 4, 4.5, 1.6);
  const opGlow = new THREE.PointLight(OP, 3, 4.5, 1.6);
  scene.add(poolGlow, opGlow);

  // ── stone ──
  const stoneMap = stoneTexture(3.1);
  stoneMap.repeat.set(1.6, 1.6);
  const stone = new THREE.MeshStandardMaterial({ map: stoneMap, roughness: 0.9, metalness: 0.02, color: '#e4e6dc' });
  const stoneDark = new THREE.MeshStandardMaterial({ map: stoneMap, roughness: 0.95, metalness: 0, color: '#aab2aa' });

  const root = new THREE.Group();
  root.position.y = 0.45;
  scene.add(root);

  const parts: Record<PartName, THREE.Object3D> = {} as Record<PartName, THREE.Object3D>;

  // Escrow shelf, top left.
  const escrow = new THREE.Mesh(rockGeometry(2.9, 0.55, 2.0, 1.7), stone);
  escrow.name = 'escrow';
  escrow.position.set(-2.7, 2.15, 0);
  root.add(escrow);
  parts.escrow = escrow;
  const escrowCliff = new THREE.Mesh(boulderGeometry(1.45, 1.9, 1.1, 5.2), stoneDark);
  escrowCliff.name = 'escrow-cliff';
  escrowCliff.userData.part = 'escrow';
  escrowCliff.position.set(-2.85, 0.72, -0.15);
  root.add(escrowCliff);
  // The escrow's own still water, sitting on the shelf.
  const escrowWaterMat = new THREE.MeshPhysicalMaterial({ color: INFLOW, roughness: 0.12, metalness: 0, clearcoat: 1, transparent: true, opacity: 0.55 });
  const escrowWater = new THREE.Mesh(new THREE.PlaneGeometry(2.3, 1.4), escrowWaterMat);
  escrowWater.name = 'escrow-water';
  escrowWater.userData.part = 'escrow';
  escrowWater.rotation.x = -Math.PI / 2;
  escrowWater.position.set(-2.55, 2.48, 0);
  root.add(escrowWater);

  // Pool basin, middle.
  const poolGroup = new THREE.Group();
  poolGroup.name = 'pool';
  poolGroup.position.set(0.15, 0.25, 0);
  const poolBowl = new THREE.Mesh(basinGeometry(1.25, 0.55), stone);
  poolBowl.name = 'pool-basin';
  poolBowl.scale.set(1, 1, 0.78);
  poolGroup.add(poolBowl);
  const poolPillar = new THREE.Mesh(boulderGeometry(1.45, 1.35, 1.15, 8.4), stoneDark);
  poolPillar.name = 'pool-pillar';
  poolPillar.position.set(0.05, -1.05, -0.05);
  poolGroup.add(poolPillar);
  const poolWaterMat = new THREE.MeshPhysicalMaterial({ color: POOL, roughness: 0.08, metalness: 0, clearcoat: 1, clearcoatRoughness: 0.05, transparent: true, opacity: 0.88, emissive: POOL, emissiveIntensity: 0.25 });
  const poolWater = new THREE.Mesh(new THREE.CircleGeometry(1, 48), poolWaterMat);
  poolWater.name = 'pool-water';
  poolWater.rotation.x = -Math.PI / 2;
  poolWater.scale.set(1, 0.78, 1);
  poolWater.userData.part = 'pool';
  root.add(poolWater);
  root.add(poolGroup);
  parts.pool = poolGroup;
  poolGlow.position.set(0.15, 0.7, 0.6);

  // Operator basin, bottom right.
  const opGroup = new THREE.Group();
  opGroup.name = 'operator';
  opGroup.position.set(2.85, -1.65, 0);
  const opBowl = new THREE.Mesh(basinGeometry(1.15, 0.5), stone);
  opBowl.name = 'operator-basin';
  opBowl.scale.set(1, 1, 0.8);
  opGroup.add(opBowl);
  const opPillar = new THREE.Mesh(boulderGeometry(1.35, 0.95, 1.1, 11.9), stoneDark);
  opPillar.name = 'operator-pillar';
  opPillar.position.set(0, -0.78, -0.05);
  opGroup.add(opPillar);
  const opWaterMat = new THREE.MeshPhysicalMaterial({ color: OP, roughness: 0.1, metalness: 0.1, clearcoat: 1, clearcoatRoughness: 0.06, transparent: true, opacity: 0.9, emissive: OP, emissiveIntensity: 0.22 });
  const opWater = new THREE.Mesh(new THREE.CircleGeometry(1, 48), opWaterMat);
  opWater.name = 'operator-water';
  opWater.rotation.x = -Math.PI / 2;
  opWater.scale.set(1, 0.8, 1);
  opWater.userData.part = 'operator';
  root.add(opWater);
  root.add(opGroup);
  parts.operator = opGroup;
  opGlow.position.set(2.85, -1.2, 0.6);

  // Scattered pebbles (instanced: repeated ornaments).
  const pebbleGeo = new THREE.IcosahedronGeometry(0.09, 1);
  const pebbles = new THREE.InstancedMesh(pebbleGeo, stoneDark, 26);
  pebbles.name = 'pebbles';
  {
    const r = mulberry(42);
    const m = new THREE.Matrix4();
    const q = new THREE.Quaternion();
    for (let i = 0; i < 26; i++) {
      const onPool = i < 14;
      const x = onPool ? 0.15 + (r() - 0.5) * 2.6 : 2.85 + (r() - 0.5) * 2.2;
      const y = onPool ? 0.02 : -1.88;
      const z = (r() > 0.5 ? 1 : -1) * (0.95 + r() * 0.25);
      const s = 0.6 + r() * 1.1;
      q.setFromEuler(new THREE.Euler(r() * 3, r() * 3, r() * 3));
      m.compose(new THREE.Vector3(x, y, z), q, new THREE.Vector3(s, s * 0.7, s));
      pebbles.setMatrixAt(i, m);
    }
  }
  root.add(pebbles);

  // Where the water sits, in root space. These are the procedural rock's numbers; a loaded
  // fountain model replaces them with its own (useModel below).
  const layout = {
    pool: { x: 0.15, rim: 0.25, floor: -0.3, z: 0, r: 1.0, rz: 0.78 },
    op: { x: 2.85, rim: -1.65, floor: -2.1, z: 0, r: 0.92, rz: 0.8 },
  };
  const rockParts: THREE.Object3D[] = [escrow, escrowCliff, poolBowl, poolPillar, opBowl, opPillar, pebbles];
  const fountainMats: THREE.MeshStandardMaterial[] = [];

  // ── water in motion ──
  const dropGeo = new THREE.SphereGeometry(0.022, 6, 4);
  dropGeo.scale(1, 3.2, 1);
  const inflowMat = new THREE.MeshBasicMaterial({ color: INFLOW, transparent: true, opacity: 0.6, depthWrite: false, blending: THREE.AdditiveBlending });
  const spillMat = new THREE.MeshBasicMaterial({ color: OP, transparent: true, opacity: 0.9, depthWrite: false, blending: THREE.AdditiveBlending });

  const A = new THREE.Vector3(-1.32, 2.42, 0); // escrow lip
  const B = new THREE.Vector3(-0.2, 0.28, 0); // pool surface
  const C = new THREE.Vector3(1.38, 0.18, 0); // pool lip
  const D = new THREE.Vector3(2.55, -1.6, 0); // operator surface

  const MAX_DROPS = 320;

  function makeStream(from: THREE.Vector3, to: THREE.Vector3, mat: THREE.Material, seed: number, name: string): Stream {
    const mesh = new THREE.InstancedMesh(dropGeo, mat, MAX_DROPS);
    mesh.name = name;
    mesh.frustumCulled = false;
    const r = mulberry(seed);
    const phase = new Float32Array(MAX_DROPS), lane = new Float32Array(MAX_DROPS), jitter = new Float32Array(MAX_DROPS);
    for (let i = 0; i < MAX_DROPS; i++) {
      phase[i] = r();
      lane[i] = r() - 0.5;
      jitter[i] = r();
    }
    root.add(mesh);
    const s: Stream = { mesh, from, to, width: 1, count: 0, phase, lane, jitter, flight: 1, vx: 0, vy0: 0 };
    aim(s);
    return s;
  }

  /** A falling arc from the stream's lip to where it lands (re-run when either point moves). */
  function aim(s: Stream) {
    const drop = Math.max(0.2, s.from.y - s.to.y);
    s.flight = Math.sqrt((2 * drop) / GRAVITY) * 1.25;
    s.vx = (s.to.x - s.from.x) / s.flight;
    s.vy0 = (s.to.y - s.from.y + 0.5 * GRAVITY * s.flight * s.flight) / s.flight;
  }

  const inflow = makeStream(A, B, inflowMat, 7, 'inflow');
  const spill = makeStream(C, D, spillMat, 19, 'spill');

  // Mist where each stream lands.
  const mistTex = mistTexture();
  let mistLift = 1; // how high spray rises above the water; lower over the sculpted bowls
  let mistScale = 1; // how strong it is
  function makeMist(at: THREE.Vector3, color: THREE.Color, name: string) {
    const n = 40;
    const geo = new THREE.BufferGeometry();
    geo.setAttribute('position', new THREE.BufferAttribute(new Float32Array(n * 3), 3));
    const mat = new THREE.PointsMaterial({ map: mistTex, color, size: 0.42, transparent: true, opacity: 0.32, depthWrite: false, blending: THREE.AdditiveBlending, sizeAttenuation: true });
    const pts = new THREE.Points(geo, mat);
    pts.name = name;
    pts.frustumCulled = false;
    root.add(pts);
    const seeds = new Float32Array(n * 3);
    const r = mulberry(name.length * 13);
    for (let i = 0; i < seeds.length; i++) seeds[i] = r();
    return { pts, at, seeds, n };
  }
  const poolMist = makeMist(B, INFLOW, 'pool-mist');
  const opMist = makeMist(D, OP, 'operator-mist');

  // Ripples spreading from each landing point.
  const ringGeo = new THREE.RingGeometry(0.2, 0.24, 40);
  function makeRipples(at: THREE.Vector3, color: THREE.Color, scaleZ: number) {
    const rings: THREE.Mesh[] = [];
    for (let i = 0; i < 3; i++) {
      const m = new THREE.Mesh(ringGeo, new THREE.MeshBasicMaterial({ color, transparent: true, opacity: 0, depthWrite: false, side: THREE.DoubleSide }));
      m.rotation.x = -Math.PI / 2;
      m.position.set(at.x, at.y + 0.012, at.z);
      m.scale.set(1, 1, 1);
      m.userData.scaleZ = scaleZ;
      root.add(m);
      rings.push(m);
    }
    return rings;
  }
  const poolRipples = makeRipples(new THREE.Vector3(B.x, 0.25, 0), POOL.clone().lerp(new THREE.Color('#ffffff'), 0.5), 0.78);
  const opRipples = makeRipples(new THREE.Vector3(D.x, -1.65, 0), OP.clone().lerp(new THREE.Color('#ffffff'), 0.4), 0.8);

  // ── data → geometry ──
  let hasMoney = false;
  function applyData() {
    const paid = Math.max(0, data.paid);
    const has = paid > 0;
    hasMoney = has;
    const opShare = has ? Math.min(1, data.op / paid) : 0;
    const poolShare = has ? Math.min(1, data.pool / paid) : 0;
    inflow.width = has ? 1.15 : 0;
    spill.width = has ? Math.max(0.12, 1.15 * opShare) : 0;
    inflow.count = has ? 320 : 0;
    spill.count = has ? Math.max(30, Math.round(320 * opShare)) : 0;
    inflow.mesh.count = inflow.count;
    spill.mesh.count = spill.count;
    // Basin levels: how full each is reads as how much of the money it kept.
    const level = (b: typeof layout.pool, share: number) => b.floor + (b.rim - b.floor) * (0.25 + 0.6 * Math.max(0.2, share));
    const ps = Math.max(0.2, poolShare), os = Math.max(0.2, opShare);
    poolWater.position.set(layout.pool.x, level(layout.pool, poolShare), layout.pool.z);
    poolWater.scale.set(layout.pool.r * (0.72 + 0.26 * ps), layout.pool.r * layout.pool.rz * (0.72 + 0.26 * ps), 1);
    poolWater.visible = has;
    opWater.position.set(layout.op.x, level(layout.op, opShare), layout.op.z);
    opWater.scale.set(layout.op.r * (0.71 + 0.26 * os), layout.op.r * layout.op.rz * (0.71 + 0.26 * os), 1);
    opWater.visible = has;
    // the streams land on the water, and the rings spread from where they land
    B.set(layout.pool.x - layout.pool.r * 0.35, poolWater.position.y, layout.pool.z);
    D.set(layout.op.x - layout.op.r * 0.25, opWater.position.y, layout.op.z);
    aim(inflow);
    aim(spill);
    for (const r of poolRipples) r.position.set(B.x, B.y + 0.012, B.z);
    for (const r of opRipples) r.position.set(D.x, D.y + 0.012, D.z);
    escrowWater.visible = has;
    poolMist.pts.visible = opMist.pts.visible = has;
    for (const r of [...poolRipples, ...opRipples]) r.visible = has;
  }
  applyData();

  const ripples = [...poolRipples, ...opRipples];
  function applyMode() {
    const P = PALETTE[mode];
    const blend = P.additive ? THREE.AdditiveBlending : THREE.NormalBlending;
    (scene.fog as THREE.Fog).color.set(P.fog);
    (scene.fog as THREE.Fog).near = P.fogNear;
    (scene.fog as THREE.Fog).far = P.fogFar;
    renderer.toneMappingExposure = P.exposure;
    hemi.color.set(P.sky);
    hemi.groundColor.set(P.ground);
    hemi.intensity = P.hemi;
    key.intensity = P.key;
    stone.color.set(P.stone);
    for (const m of fountainMats) m.color.set(mode === 'day' ? '#ffffff' : '#8b8f97');
    stoneDark.color.set(P.stoneDark);
    poolWaterMat.color.set(P.pool);
    poolWaterMat.emissive.set(P.pool);
    opWaterMat.color.set(P.op);
    opWaterMat.emissive.set(P.op);
    poolGlow.color.set(P.pool);
    opGlow.color.set(P.op);
    inflowMat.color.set(P.inflow);
    spillMat.color.set(P.op);
    escrowWaterMat.color.set(P.inflow);
    for (const m of [inflowMat, spillMat]) {
      m.blending = blend;
      m.needsUpdate = true;
    }
    for (const mist of [poolMist, opMist]) {
      const mm = mist.pts.material as THREE.PointsMaterial;
      mm.color.set(mist === opMist ? P.op : P.mist);
      mm.blending = blend;
      mm.needsUpdate = true;
    }
    ripples.forEach((r, i) => {
      const mm = r.material as THREE.MeshBasicMaterial;
      mm.color.set(i < poolRipples.length ? (mode === 'day' ? '#ffffff' : P.pool) : (mode === 'day' ? '#fff3e6' : P.op));
    });
  }
  applyMode();

  // ── per-frame ──
  const m4 = new THREE.Matrix4();
  const q = new THREE.Quaternion();
  const sc = new THREE.Vector3(1, 1, 1);
  const v = new THREE.Vector3();
  const up = new THREE.Vector3(0, 1, 0);
  const vel = new THREE.Vector3();

  function updateStream(s: Stream, t: number, speed: number) {
    for (let i = 0; i < s.count; i++) {
      const life = (s.phase[i]! + t * speed * (0.85 + s.jitter[i]! * 0.3)) % 1;
      const tt = life * s.flight;
      const spread = s.width * (0.4 + 0.6 * life); // the sheet fans out as it falls
      v.set(
        s.from.x + s.vx * tt + (s.jitter[i]! - 0.5) * 0.08,
        s.from.y + s.vy0 * tt - 0.5 * GRAVITY * tt * tt,
        s.from.z + s.lane[i]! * spread,
      );
      vel.set(s.vx, s.vy0 - GRAVITY * tt, 0).normalize();
      q.setFromUnitVectors(up, vel.negate());
      const k = 0.7 + s.jitter[i]! * 0.6;
      sc.set(k, k * (0.8 + life * 0.8), k);
      m4.compose(v, q, sc);
      s.mesh.setMatrixAt(i, m4);
    }
    s.mesh.instanceMatrix.needsUpdate = true;
  }

  function updateMist(m: ReturnType<typeof makeMist>, t: number, width: number) {
    const arr = (m.pts.geometry.attributes.position as THREE.BufferAttribute).array as Float32Array;
    for (let i = 0; i < m.n; i++) {
      const life = (m.seeds[i * 3]! + t * 0.25) % 1;
      arr[i * 3] = m.at.x + (m.seeds[i * 3 + 1]! - 0.5) * 0.9 * Math.max(0.4, mistScale);
      arr[i * 3 + 1] = m.at.y + life * 0.7 * mistLift;
      arr[i * 3 + 2] = (m.seeds[i * 3 + 2]! - 0.5) * Math.max(0.3, width);
    }
    m.pts.geometry.attributes.position!.needsUpdate = true;
    (m.pts.material as THREE.PointsMaterial).opacity = (0.18 + 0.2 * Math.min(1, width)) * mistScale;
  }

  function updateRipples(rings: THREE.Mesh[], t: number, strength: number) {
    rings.forEach((r, i) => {
      const life = (t * 0.6 + i / rings.length) % 1;
      const s = 0.6 + life * 3.2;
      r.scale.set(s, s * (r.userData.scaleZ as number), s);
      (r.material as THREE.MeshBasicMaterial).opacity = (1 - life) * 0.55 * strength;
    });
  }

  // Pointer parallax and picking.
  const pointer = new THREE.Vector2(0, 0);
  const target = new THREE.Vector2(0, 0);
  const ray = new THREE.Raycaster();
  const pickables: THREE.Object3D[] = [escrow, escrowCliff, escrowWater, poolBowl, poolPillar, poolWater, opBowl, opPillar, opWater];

  function partOf(o: THREE.Object3D | null): PartName | null {
    let n: THREE.Object3D | null = o;
    while (n) {
      if (n.userData.part) return n.userData.part as PartName;
      if (n.name === 'escrow' || n.name === 'pool' || n.name === 'operator') return n.name as PartName;
      n = n.parent;
    }
    return null;
  }

  function pick(clientX: number, clientY: number): PartName | null {
    const rect = canvas.getBoundingClientRect();
    const ndc = new THREE.Vector2(((clientX - rect.left) / rect.width) * 2 - 1, -((clientY - rect.top) / rect.height) * 2 + 1);
    ray.setFromCamera(ndc, camera);
    const hit = ray.intersectObjects(pickables, true)[0];
    if (!hit) return null;
    if (hit.object.userData.fountain) {
      // one sculpted mesh: which part was hit is where on it the pointer landed
      const pnt = root.worldToLocal(hit.point.clone());
      const near = (b: typeof layout.pool) => Math.hypot(pnt.x - b.x, (pnt.z - b.z) / Math.max(0.5, b.rz)) < b.r * 1.35;
      if (near(layout.op) && pnt.y < layout.pool.floor) return 'operator';
      if (near(layout.pool)) return 'pool';
      if (near(layout.op)) return 'operator';
      return pnt.x < layout.pool.x ? 'escrow' : null;
    }
    return partOf(hit.object);
  }

  const onMove = (e: PointerEvent) => {
    const rect = canvas.getBoundingClientRect();
    target.set(((e.clientX - rect.left) / rect.width) * 2 - 1, ((e.clientY - rect.top) / rect.height) * 2 - 1);
    const h = pick(e.clientX, e.clientY);
    if (h !== hovered) {
      hovered = h;
      canvas.style.cursor = h ? 'pointer' : '';
      if (reduced || paused) renderOnce();
    }
  };
  const onLeave = () => {
    target.set(0, 0);
    hovered = null;
    canvas.style.cursor = '';
  };
  const onClick = (e: MouseEvent) => {
    const p = pick(e.clientX, e.clientY);
    selected = p === selected ? null : p;
    opts.onPick?.(selected);
    renderOnce();
  };
  canvas.addEventListener('pointermove', onMove);
  canvas.addEventListener('pointerleave', onLeave);
  canvas.addEventListener('click', onClick);

  // ── highlight: the selected or hovered part lifts its glow ──
  function applyHighlight() {
    const focus = selected ?? hovered;
    // Dry basins do not glow: with nothing settled there is no money in them to light.
    const lit = hasMoney ? 1 : 0;
    poolGlow.intensity = (focus === 'pool' ? 9 : 4) * (1 + surge * 0.6) * lit;
    opGlow.intensity = (focus === 'operator' ? 8 : 3) * (1 + surge * 0.4) * lit;
    poolWaterMat.emissiveIntensity = focus === 'pool' ? 0.6 : 0.25 + surge * 0.2;
    opWaterMat.emissiveIntensity = focus === 'operator' ? 0.55 : 0.22 + surge * 0.15;
    escrowWaterMat.opacity = focus === 'escrow' ? 0.85 : 0.55;
    stone.emissive.set(focus === 'escrow' ? '#15302c' : '#000000');
  }

  // ── sizing ──
  function resize() {
    const w = Math.max(1, container.clientWidth);
    const h = Math.max(1, container.clientHeight);
    renderer.setSize(w, h, false);
    canvas.style.width = `${w}px`;
    canvas.style.height = `${h}px`;
    camera.aspect = w / h;
    // Narrow frames pull back so all three shelves stay in view.
    const narrow = w / h < 1.25;
    camera.fov = narrow ? 41 : 31;
    camBase.set(narrow ? 0.5 : 1.0, narrow ? 5.0 : 4.6, narrow ? 14.2 : 13.2);
    camera.updateProjectionMatrix();
    if (reduced || paused || !running) renderOnce();
  }
  const ro = new ResizeObserver(resize);
  ro.observe(container);

  // ── anchors for the HTML labels ──
  const anchorPts: Record<PartName, THREE.Vector3> = {
    escrow: new THREE.Vector3(-2.7, 2.6, 0.6),
    pool: new THREE.Vector3(0.15, 0.85, 0.5),
    operator: new THREE.Vector3(2.85, -1.05, 0.5),
  };
  const proj = new THREE.Vector3();
  function emitAnchors() {
    if (!opts.onFrame) return;
    const w = container.clientWidth, h = container.clientHeight;
    const out = {} as Anchors;
    for (const k of Object.keys(anchorPts) as PartName[]) {
      proj.copy(anchorPts[k]).applyMatrix4(root.matrixWorld).project(camera);
      out[k] = { x: (proj.x * 0.5 + 0.5) * w, y: (-proj.y * 0.5 + 0.5) * h, visible: proj.z < 1 };
    }
    opts.onFrame(out);
  }

  // ── loop ──
  const t0 = performance.now();
  let tStill = 0.37; // the frozen moment shown under reduced motion

  function frame(now: number, still: boolean) {
    const t = still ? tStill : (now - t0) / 1000;
    if (!still) {
      // ease the camera towards the pointer: a few degrees, never a spin
      pointer.lerp(target, 0.05);
      surge *= 0.965;
      if (data.jobs.length > 1 && now - lastSwitch > REPLAY_MS) showJob((jobIndex + 1) % data.jobs.length, now);
    }
    camera.position.set(camBase.x + pointer.x * 0.9, camBase.y - pointer.y * 0.5, camBase.z);
    camera.lookAt(look);
    root.rotation.y = -0.18 + pointer.x * 0.06;
    root.updateMatrixWorld();
    const speed = 0.55 * (1 + surge * 0.8);
    if (inflow.count) updateStream(inflow, t, speed);
    if (spill.count) updateStream(spill, t + 0.31, speed);
    if (inflow.count) {
      updateMist(poolMist, t, inflow.width);
      updateMist(opMist, t + 0.5, spill.width);
      updateRipples(poolRipples, t, 1 + surge * 0.6);
      updateRipples(opRipples, t + 0.2, Math.max(0.35, spill.width / 1.15) * (1 + surge * 0.4));
    }
    const base = mode === 'day' ? 0.7 : 0.5;
    inflowMat.opacity = base + surge * 0.3;
    spillMat.opacity = base + 0.1 + surge * 0.25;
    applyHighlight();
    renderer.render(scene, camera);
    emitAnchors();
  }

  function renderOnce() {
    if (disposed) return;
    frame(performance.now(), reduced || paused);
  }

  function loop(now: number) {
    if (!running) return;
    frame(now, false);
    raf = requestAnimationFrame(loop);
  }

  function start() {
    if (running || reduced || paused || !visible || disposed) return;
    running = true;
    raf = requestAnimationFrame(loop);
  }
  function stop() {
    running = false;
    cancelAnimationFrame(raf);
  }

  function showJob(i: number, now = performance.now()) {
    if (!data.jobs.length) return;
    jobIndex = ((i % data.jobs.length) + data.jobs.length) % data.jobs.length;
    lastSwitch = now;
    surge = 1;
    opts.onJob?.(jobIndex);
    if (reduced || paused) renderOnce();
  }

  // Pause off screen and in a hidden tab: no loop runs that nobody can see.
  const io = new IntersectionObserver((entries) => {
    visible = entries.some((e) => e.isIntersecting) && !document.hidden;
    if (visible) start();
    else stop();
  });
  io.observe(container);
  const onVis = () => {
    visible = !document.hidden;
    if (visible) start();
    else stop();
  };
  document.addEventListener('visibilitychange', onVis);

  // ── the sculpted fountain (Higgsfield → Meshy image-to-3D, meshopt + WebP) ──
  function useModel(model: THREE.Object3D) {
    const F = FOUNTAIN;
    model.scale.setScalar(F.scale);
    model.rotation.y = F.rotationY;
    model.position.copy(F.offset);
    model.traverse((o) => {
      const mesh = o as THREE.Mesh;
      if (!mesh.isMesh) return;
      mesh.userData.fountain = true;
      const mats = Array.isArray(mesh.material) ? mesh.material : [mesh.material];
      for (const m of mats) {
        const sm = m as THREE.MeshStandardMaterial;
        if (sm.isMeshStandardMaterial) {
          // Limestone is not metal: drop the generated metalness map, which picked up the sky as
          // blue smears, and keep the surface matte.
          sm.metalnessMap = null;
          sm.metalness = 0;
          sm.roughness = 1;
          sm.envMapIntensity = 0.35;
          sm.needsUpdate = true;
          fountainMats.push(sm);
        }
      }
      pickables.push(mesh);
    });
    root.add(model);
    model.updateMatrix();
    const toRoot = (v: THREE.Vector3) => v.clone().applyMatrix4(model.matrix);
    for (const r of rockParts) r.visible = false;

    const P = toRoot(new THREE.Vector3(F.pool.x, F.pool.rim, F.pool.z));
    const Pf = toRoot(new THREE.Vector3(F.pool.x, F.pool.floor, F.pool.z));
    const O = toRoot(new THREE.Vector3(F.op.x, F.op.rim, F.op.z));
    const Of = toRoot(new THREE.Vector3(F.op.x, F.op.floor, F.op.z));
    layout.pool = { x: P.x, rim: P.y, floor: Pf.y, z: P.z, r: F.pool.r * F.scale, rz: F.pool.rz };
    layout.op = { x: O.x, rim: O.y, floor: Of.y, z: O.z, r: F.op.r * F.scale, rz: F.op.rz };
    A.copy(toRoot(F.shelfLip));
    C.copy(toRoot(F.poolLip));
    const ew = toRoot(new THREE.Vector3(F.escrowWater.x, F.escrowWater.y, F.escrowWater.z));
    escrowWater.position.copy(ew);
    escrowWater.scale.set((F.escrowWater.w * F.scale) / 2.3, (F.escrowWater.d * F.scale) / 1.4, 1);
    anchorPts.escrow.set(ew.x, ew.y + 0.55, ew.z + 0.3);
    anchorPts.pool.set(P.x, P.y + 0.7, P.z + 0.3);
    anchorPts.operator.set(O.x, O.y + 0.75, O.z + 0.3);
    // the glow sits just above each bowl's water and reaches only the bowl, not the shelf above it
    poolGlow.position.set(P.x, (P.y + Pf.y) / 2 + 0.1, P.z + 0.15);
    opGlow.position.set(O.x, (O.y + Of.y) / 2 + 0.1, O.z + 0.15);
    poolGlow.distance = 1.6;
    opGlow.distance = 1.9;
    mistLift = 0.18;
    mistScale = 0.45;
    for (const mist of [poolMist, opMist]) (mist.pts.material as THREE.PointsMaterial).size = 0.16;
    for (const r of poolRipples) r.userData.scaleZ = F.pool.rz;
    for (const r of opRipples) r.userData.scaleZ = F.op.rz;
    applyData();
    applyMode();
    if (debug) markAnchors();
    renderOnce();
  }

  // ?cascadeDebug shows the anchor points the water and labels are attached to.
  const debug = typeof window !== 'undefined' && window.location.search.includes('cascadeDebug');
  const markers = new THREE.Group();
  root.add(markers);
  function markAnchors() {
    markers.clear();
    const dot = (v: THREE.Vector3, c: string) => {
      const m = new THREE.Mesh(new THREE.SphereGeometry(0.06, 10, 8), new THREE.MeshBasicMaterial({ color: c, depthTest: false }));
      m.position.copy(v);
      m.renderOrder = 10;
      markers.add(m);
    };
    dot(A, '#ff0000');
    dot(C, '#ff00ff');
    dot(new THREE.Vector3(layout.pool.x, layout.pool.rim, layout.pool.z), '#00a0ff');
    dot(new THREE.Vector3(layout.pool.x, layout.pool.floor, layout.pool.z), '#003a80');
    dot(new THREE.Vector3(layout.op.x, layout.op.rim, layout.op.z), '#ffa000');
    dot(new THREE.Vector3(layout.op.x, layout.op.floor, layout.op.z), '#804000');
  }
  if (debug) markAnchors();

  if (opts.modelUrl) {
    const loader = new GLTFLoader();
    loader.setMeshoptDecoder(MeshoptDecoder);
    loader.load(
      opts.modelUrl,
      (gltf) => {
        if (!disposed) useModel(gltf.scene);
      },
      undefined,
      () => {
        // keep the procedural rock: the stage still works without the sculpture
      },
    );
  }

  resize();
  if (data.jobs.length) opts.onJob?.(0);
  renderOnce();
  start();

  return {
    setData(d) {
      data = d;
      applyData();
      if (jobIndex >= d.jobs.length) jobIndex = 0;
      renderOnce();
    },
    setPaused(p) {
      paused = p;
      if (p) {
        stop();
        tStill = (performance.now() - t0) / 1000;
        renderOnce();
      } else {
        lastSwitch = performance.now();
        start();
      }
    },
    setReduced(r) {
      reduced = r;
      if (r) {
        stop();
        renderOnce();
      } else start();
    },
    setSelected(p) {
      selected = p;
      renderOnce();
    },
    setMode(m) {
      if (m === mode) return;
      mode = m;
      applyMode();
      renderOnce();
    },
    showJob(i) {
      showJob(i);
    },
    dispose() {
      disposed = true;
      stop();
      io.disconnect();
      ro.disconnect();
      document.removeEventListener('visibilitychange', onVis);
      canvas.removeEventListener('pointermove', onMove);
      canvas.removeEventListener('pointerleave', onLeave);
      canvas.removeEventListener('click', onClick);
      canvas.removeEventListener('webglcontextlost', onLost);
      scene.traverse((o) => {
        const mesh = o as THREE.Mesh;
        if (mesh.geometry) mesh.geometry.dispose();
        const mat = mesh.material as THREE.Material | THREE.Material[] | undefined;
        const all = Array.isArray(mat) ? mat : mat ? [mat] : [];
        for (const m of all) {
          for (const v of Object.values(m)) if (v instanceof THREE.Texture) v.dispose();
          m.dispose();
        }
      });
      stoneMap.dispose();
      mistTex.dispose();
      envTex.dispose();
      pmrem.dispose();
      renderer.dispose();
      canvas.remove();
    },
  };
}
