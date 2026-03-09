# Execution Order

This file defines the recommended implementation order for the planning workstreams under `planning/`.

The sequence is optimized for the stated goal:

- keep the system as lightweight as possible
- improve security first at the correct architectural boundary
- avoid overengineering or adding large new subsystems
- defer heavier operational work until the runtime and image model are correct

## Recommended order

### 1. Runtime class boundary

Start here:
- `planning/runtime-class-boundary/`

Why first:
- this sets the core production boundary correctly
- it prevents further investment in plain Docker as the hostile isolation layer
- it creates the small abstraction needed to support trusted Docker and VM-backed runtimes cleanly

Primary outcomes:
- production is VM-backed only
- Docker is clearly `trusted-docker`
- backend policy and runtime policy stop being conflated

### 2. Trusted Docker hardening

Then:
- `planning/trusted-docker-hardening/`

Why second:
- Docker still exists as a development and compatibility path
- once the boundary is explicit, the Docker path can be tightened without pretending it is the production security product
- this yields quick security wins with limited implementation scope

Primary outcomes:
- non-root by default
- `cap-drop=ALL`
- `no-new-privileges`
- read-only rootfs and minimal writable mounts
- explicit denial of dangerous Docker modes

### 3. Lightweight image profiles

Then:
- `planning/lightweight-image-profiles/`

Why third:
- after the runtime boundary is correct and the trusted Docker path is tightened, the next biggest lightweight/security win is shrinking the default image path
- this reduces attack surface, pull time, boot time, and resource cost
- it also removes broad tooling like browser stacks and inner Docker from the default path

Primary outcomes:
- smaller default image
- explicit `core` / `runtime` / `browser` / `container` / `debug` profile usage
- browser and inner Docker only when explicitly requested

### 4. Storage, network, and snapshot hardening

Then:
- `planning/storage-network-snapshot-hardening/`

Why fourth:
- once runtime and image shape are stable, storage and networking rules can be tightened around the real sandbox model
- this avoids designing quotas, mounts, and snapshot validation around the wrong base assumptions
- this is where real disk enforcement and restore safety become practical to land cleanly

Primary outcomes:
- explicit storage classes
- tighter writable surface
- backend-aware storage enforcement
- policy-driven networking
- hostile snapshot input validation

### 5. Production ops hardening

Finish with:
- `planning/production-ops-hardening/`

Why last:
- admission control, fairness, recovery drills, telemetry, and supply-chain policy are most effective after runtime, image, and storage behavior are settled
- this avoids building release gates and observability around unstable internals
- it keeps the operational layer small and focused on the design that actually ships

Primary outcomes:
- bounded admission control
- stronger restart and reconciliation guarantees
- abuse-resistant release validation
- audit-grade telemetry using existing repo surfaces
- tighter curated-image policy

## Dependency summary

### Hard dependencies

- `trusted-docker-hardening` depends on `runtime-class-boundary`
- `lightweight-image-profiles` depends on `runtime-class-boundary`
- `storage-network-snapshot-hardening` depends on `runtime-class-boundary`
- `production-ops-hardening` depends on all prior workstreams for the cleanest implementation

### Soft dependencies

- `lightweight-image-profiles` and `trusted-docker-hardening` can overlap in limited areas once runtime-class terminology is settled
- some storage layout work can begin in parallel with image-profile cleanup if the mount model remains aligned to the runtime-boundary plan

## Suggested delivery phases

### Phase 1: boundary and immediate risk reduction

- `runtime-class-boundary`
- `trusted-docker-hardening`

### Phase 2: reduce weight and attack surface

- `lightweight-image-profiles`

### Phase 3: tighten persistence and exposure controls

- `storage-network-snapshot-hardening`

### Phase 4: prove production behavior

- `production-ops-hardening`

## Smallest secure first release

If implementation needs to be cut to the smallest high-value release, ship in this order:

1. runtime boundary fix
2. trusted Docker least-privilege hardening
3. smaller default images and explicit profiles
4. real storage/snapshot safeguards
5. recovery, abuse testing, and telemetry polish

## Explicit non-goals for ordering

This order intentionally avoids making these the first move:

- a large containerd migration before the runtime boundary is expressed in code
- a new distributed scheduler
- a complex firewall or SDN subsystem
- a broad image catalog service
- productionizing plain Docker through incremental flag additions alone
