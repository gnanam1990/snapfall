# ABI freeze log

Freeze date: **Fri 24 Jul 2026** (PRD §14.4 rule 2). After that date, changes need all-three
agreement and a version bump. Additions before the freeze are recorded here.

## 19 Jul 2026 — SPEC-01 + SPEC-04 rulings

**No signature CHANGES.** Every entry below is an addition; nothing pre-existing was altered.
`requestAdvance`, `repayAdvance`, `writeOff`, `acceptDelivery`, `refund`, `cancel`, `deposit`,
and `withdraw` were implemented against the signatures already declared in PRD §7.

### JobVault — added functions
| Signature | Why |
| --- | --- |
| `wireFloatPool(address)` | SPEC-04. Admin-only, one-shot. The waterfall cannot run unwired. |

### JobVault — added events
| Signature |
| --- |
| `Wired(address indexed floatPool)` |

### JobVault — added errors
`AlreadyWired()`, `NotWired()`
(plus `JobExists()`, `UnknownJob()`, `ZeroAddress()`, `ZeroAmount()`, `ZeroHash()` earlier on 19 Jul)

### FloatPool — added functions
| Signature | Why |
| --- | --- |
| `wireJobVault(address)` | SPEC-04. Admin-only, one-shot. SC-FP-010 depends on it. |
| `sharesOf(address)` | LP share accounting (auto-getter on a new public mapping). |
| `totalShares()` | LP share accounting (auto-getter). |

### FloatPool — added events
| Signature | Why |
| --- | --- |
| `Wired(address indexed jobVault)` | SPEC-04. |
| `BondSlashed(bytes32 indexed jobId, uint256 amount)` | SC-FP-008 stage 1. |
| `ReserveDrawn(bytes32 indexed jobId, uint256 amount)` | SC-FP-008 stage 2. |
| `LossSocialized(bytes32 indexed jobId, uint256 amount)` | SC-FP-008 stage 3. |

The pre-existing summary event `AdvanceWrittenOff(jobId, bondSlashed, reserveUsed, socialized)`
is **unchanged** and still emitted; the three stage events satisfy the "an event per stage"
requirement without touching it.

### FloatPool — added errors
`NotAuthorized()`, `ZeroAddress()`, `AlreadyWired()`, `NotWired()`, `NoOpenAdvance()`,
`WrongRepayment()`, `ZeroAmount()`, `InsufficientLiquidity()`

### Behavioural change (not a signature change)
`SC-FP-002`: `requestAdvance` no longer applies `min(maxOperatingBudget, …)`. Same signature,
same return type — but the returned amount is larger for any job whose operating budget is
below `rate × customerPayment`. Indexers and the dashboard need no ABI update; the daemon's
expected-advance calculation does.
