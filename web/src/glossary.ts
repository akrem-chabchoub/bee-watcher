// Glossary for the right-click "Explain" popover. Each entry has a plain
// explanation (`kid`) and a precise one for Bee engineers (`engineer`).
// Engineer texts reference Bee packages (github.com/ethersphere/bee/pkg/…).

export interface GlossaryEntry {
  id: string;
  term: string;
  aliases?: string[];
  kid: string;
  engineer: string;
  seeAlso?: string[];
}

export const glossary: GlossaryEntry[] = [
  // ---- Addresses and topology ----
  {
    id: "overlay",
    term: "Overlay address",
    aliases: ["overlay", "overlays"],
    kid: "Every Bee node has a house number in a giant neighborhood map. Nodes with similar numbers live close together and look after the same things.",
    engineer:
      "The node's 32-byte Swarm address, derived from its Ethereum address, the network ID and a nonce (pkg/crypto, pkg/swarm). Distance between overlays is XOR; chunks are stored by the nodes whose overlays share the longest prefix with the chunk address. GET /addresses returns it.",
    seeAlso: ["proximity-order", "neighborhood", "kademlia"],
  },
  {
    id: "proximity-order",
    term: "Proximity order (bin)",
    aliases: ["proximity order", "po", "bin", "bins"],
    kid: "How many first digits two house numbers share. The more they share, the closer the neighbors.",
    engineer:
      "PO(a, b) = number of leading bits shared by two addresses (pkg/swarm Proximity). Kademlia groups peers into bins by PO 0…31; bin n holds peers that share exactly n leading bits with us. /topology reports per-bin population and connected counts.",
    seeAlso: ["kademlia", "depth", "overlay"],
  },
  {
    id: "kademlia",
    term: "Kademlia",
    aliases: ["kademlia", "topology"],
    kid: "Each node keeps a phone book with a few friends far away and many friends nearby, so any message can hop quickly to the right place.",
    engineer:
      "pkg/topology/kademlia: the connectivity driver. It keeps a saturation target of connected peers per bin, prefers balanced bins, and computes the neighborhood depth. Exposed at GET /topology (population, connected, depth, per-bin peers).",
    seeAlso: ["proximity-order", "depth", "hive", "libp2p"],
  },
  {
    id: "depth",
    term: "Depth",
    aliases: ["depth", "neighborhood depth", "kademlia depth"],
    kid: "How far out a node's closest group of neighbors reaches. Inside that circle, everybody knows everybody.",
    engineer:
      "Kademlia neighborhood depth: the lowest PO from which the node is connected to all known peers (bounded by saturation). It defines the nearest neighborhood used for routing. Not the same as the storage radius, which comes from the reserve (pkg/storer).",
    seeAlso: ["storage-radius", "neighborhood", "kademlia"],
  },
  {
    id: "neighborhood",
    term: "Neighborhood",
    aliases: ["neighborhood", "neighbourhood", "neighborhoods"],
    kid: "A small group of nodes with similar house numbers that share the job of keeping the same boxes safe.",
    engineer:
      "The set of nodes whose overlays share a prefix of length ≥ storage radius. All of them should hold the same reserve content (replication by pullsync). Redistribution selects one neighborhood per round by the anchor.",
    seeAlso: ["storage-radius", "pullsync", "redistribution"],
  },
  {
    id: "storage-radius",
    term: "Storage radius",
    aliases: ["storage radius", "radius", "committed depth"],
    kid: "How big a slice of the world's data a node promises to keep. Smaller number = bigger slice.",
    engineer:
      "pkg/storer reserve: the PO a node stores chunks at (IsWithinStorageRadius). It increases when the reserve fills up (evicting the farthest bin) and decreases when the reserve is under-utilized. /status reports storageRadius (and committedDepth). Nodes in a neighborhood should agree on it; salud penalizes outliers.",
    seeAlso: ["reserve", "depth", "salud"],
  },
  {
    id: "reserve",
    term: "Reserve",
    aliases: ["reserve", "reserve size"],
    kid: "The node's big storage shelf for other people's data in its neighborhood.",
    engineer:
      "pkg/storer/internal/reserve: the bounded store of stamped chunks within the storage radius, filled by pushsync and pullsync. Capacity is fixed (2^22 chunks by default); /status reports reserveSize and reserveSizeWithinRadius. Redistribution samples it to prove storage.",
    seeAlso: ["storage-radius", "pullsync", "redistribution"],
  },
  {
    id: "hive",
    term: "Hive",
    aliases: ["hive"],
    kid: "Nodes gossip about who else they know, so newcomers can find friends.",
    engineer: "pkg/hive: the peer-discovery protocol. Peers exchange BzzAddresses (overlay + underlays + signature) so Kademlia learns about new peers to dial.",
    seeAlso: ["kademlia", "libp2p", "bootnode"],
  },
  {
    id: "libp2p",
    term: "libp2p",
    aliases: ["libp2p", "p2p", "underlay"],
    kid: "The phone line nodes use to talk to each other.",
    engineer:
      "pkg/p2p/libp2p: transport, handshake and streams for all Bee protocols (pushsync, pullsync, retrieval, hive, pricing…). Underlay addresses are multiaddrs; the bzz handshake exchanges overlays and checks the network ID.",
    seeAlso: ["hive", "kademlia"],
  },
  {
    id: "bootnode",
    term: "Bootnode",
    aliases: ["bootnode", "bootnodes"],
    kid: "The first friend every new node calls to get introduced to everybody else.",
    engineer:
      "A node started with bootnode mode and fixed keys; others dial it from their --bootnode multiaddr. It does not store a reserve. In beekeeper configs it is the 'bootnode' node group with explicit libp2p and swarm keys.",
    seeAlso: ["hive", "full-node"],
  },
  {
    id: "full-node",
    term: "Full node",
    aliases: ["full node", "full nodes"],
    kid: "A node that both stores data for others and helps move it around.",
    engineer: "full-node: true. Runs the reserve, pullsync, redistribution (with stake) and serves retrieval for its neighborhood. Needs a chain backend and a funded wallet.",
    seeAlso: ["light-node", "reserve", "stake"],
  },
  {
    id: "light-node",
    term: "Light node",
    aliases: ["light node", "light nodes", "light"],
    kid: "A node that can upload and download, but doesn't keep other people's data.",
    engineer:
      "full-node: false with a chain backend. Connects to a few full peers (pkg/topology/lightnode), can buy stamps and upload/download, does not sync a reserve or take part in redistribution.",
    seeAlso: ["full-node", "ultra-light"],
  },
  {
    id: "ultra-light",
    term: "Ultra-light node",
    aliases: ["ultra-light", "ultralight", "ultra light"],
    kid: "A node with no wallet: it can only download, and only a little for free.",
    engineer: "No blockchain endpoint (swap disabled): cannot buy stamps or upload; downloads rely on free accounting thresholds of its peers.",
    seeAlso: ["light-node", "accounting"],
  },

  // ---- Data ----
  {
    id: "chunk",
    term: "Chunk",
    aliases: ["chunk", "chunks"],
    kid: "Files are cut into tiny 4 KB puzzle pieces. Each piece is a chunk, and its name is a fingerprint of what's inside.",
    engineer:
      "The unit of storage: up to 4096 bytes of payload plus an 8-byte span. Content-addressed chunks (CAC, pkg/cac) are addressed by the BMT hash (pkg/bmt); single-owner chunks (SOC, pkg/soc) by hash(id, owner).",
    seeAlso: ["cac-soc", "reference", "postage-stamp"],
  },
  {
    id: "cac-soc",
    term: "CAC / SOC",
    aliases: ["cac", "soc", "single owner chunk", "content addressed chunk"],
    kid: "Most pieces are named by their content. A few are named by their owner, like a mailbox only you can write in.",
    engineer:
      "Content-addressed chunk: address = BMT hash of span+payload (pkg/cac). Single-owner chunk: address = keccak256(id ‖ owner), payload signed by the owner (pkg/soc); used by feeds and GSOC.",
    seeAlso: ["chunk"],
  },
  {
    id: "reference",
    term: "Reference",
    aliases: ["reference", "references", "swarm hash", "root hash"],
    kid: "The secret code that lets anyone get your file back from the network.",
    engineer:
      "The root address of an uploaded file or manifest: 32 bytes (64 hex), or 64 bytes (128 hex) when encrypted (address + decryption key). Returned by POST /bzz and /bytes; fetched with GET /bzz/{ref}.",
    seeAlso: ["manifest", "encryption", "chunk"],
  },
  {
    id: "manifest",
    term: "Manifest (mantaray)",
    aliases: ["manifest", "mantaray"],
    kid: "A table of contents that maps names like 'index.html' to the pieces of a file.",
    engineer:
      "pkg/manifest/mantaray: a compacted trie of path → entry (reference + metadata such as Content-Type, filename). /bzz uploads wrap files in a manifest; /bzz/{ref}/{path} resolves through it.",
    seeAlso: ["reference"],
  },
  {
    id: "tag",
    term: "Tag (upload progress)",
    aliases: ["tag", "tags", "upload tag"],
    kid: "A tracking number for your upload that counts how many pieces have arrived safely.",
    engineer:
      "Per-upload counters in the storer (split, seen, stored, sent, synced). GET /tags/{uid}; the upload response carries Swarm-Tag. synced reaches split when every chunk got a receipt from its neighborhood.",
    seeAlso: ["deferred", "pushsync"],
  },
  {
    id: "deferred",
    term: "Deferred upload",
    aliases: ["deferred", "deferred upload"],
    kid: "Your node first keeps the file itself and sends it out to the world afterwards, so you don't have to wait.",
    engineer:
      "Swarm-Deferred-Upload: true (default). Chunks go to the local upload store and the pusher (pkg/pusher) pushes them asynchronously; with false, the API waits for pushsync receipts (direct upload).",
    seeAlso: ["tag", "pushsync"],
  },
  {
    id: "pinning",
    term: "Pinning",
    aliases: ["pin", "pinning", "pinned", "pins"],
    kid: "A 'do not throw away' sticker: your node keeps its own copy no matter what.",
    engineer: "Swarm-Pin: true stores the chunks in the local pin store (pkg/storer/internal/pinning), exempt from reserve eviction. Managed via /pins.",
    seeAlso: ["reserve"],
  },
  {
    id: "encryption",
    term: "Encryption",
    aliases: ["encrypt", "encryption", "encrypted"],
    kid: "Your file is locked before it leaves; only people with the full code can open it.",
    engineer:
      "Swarm-Encrypt: true encrypts every chunk with a random key (pkg/encryption); references become 64 bytes (address ‖ key). Storers cannot read the content.",
    seeAlso: ["reference"],
  },
  {
    id: "redundancy",
    term: "Redundancy level (erasure coding)",
    aliases: ["redundancy", "erasure coding", "redundancy level"],
    kid: "Extra spare pieces so the file can be rebuilt even if some pieces get lost.",
    engineer:
      "Swarm-Redundancy-Level 0–4 (none, medium, strong, insane, paranoid; pkg/file/redundancy). Adds Reed–Solomon parity chunks per intermediate chunk so retrieval tolerates missing chunks, at the cost of more chunks to stamp.",
    seeAlso: ["chunk", "retrieval"],
  },

  // ---- Protocols ----
  {
    id: "pushsync",
    term: "Pushsync",
    aliases: ["pushsync", "push sync", "push-sync"],
    kid: "Delivering a new piece to the neighborhood that should keep it, and getting a signed receipt back.",
    engineer:
      "pkg/pushsync: forwards a stamped chunk hop by hop toward its address; the closest storer replies with a signed receipt. The pusher (pkg/pusher) drives it for uploads. Metrics: bee_pushsync_*.",
    seeAlso: ["pullsync", "deferred", "chunk"],
  },
  {
    id: "pullsync",
    term: "Pullsync",
    aliases: ["pullsync", "pull sync", "pull-sync", "puller", "historical sync", "live sync"],
    kid: "Neighbors compare shelves and copy what they're missing, so everyone in the group has the same boxes.",
    engineer:
      "pkg/pullsync + pkg/puller: neighbors exchange chunk ranges per bin within the storage radius. Historical sync catches up to a cursor; live sync follows new arrivals. Stalls show as no rate in bee_pullsync_* while depth is stable.",
    seeAlso: ["reserve", "neighborhood", "pushsync"],
  },
  {
    id: "retrieval",
    term: "Retrieval",
    aliases: ["retrieval", "retrieve", "download"],
    kid: "Asking around the network for a piece until someone who has it sends it back.",
    engineer:
      "pkg/retrieval: forwarding Kademlia request toward the chunk address; the responder returns the chunk plus its stamp. Netstore (pkg/storer netstore) checks local storage first. Failures: bee_retrieval_request_failure_count.",
    seeAlso: ["chunk", "accounting"],
  },

  // ---- Postage ----
  {
    id: "postage-stamp",
    term: "Postage stamp",
    aliases: ["postage stamp", "postage", "stamp", "stamps", "postage stamps"],
    kid: "Like a prepaid ticket that lets you store letters in the shared library for a while. No ticket, no storage.",
    engineer:
      "pkg/postage: every chunk must carry a stamp signed by a batch owner (batch ID, index, timestamp, signature). Storers verify it against the batch store (from chain events) and drop unstamped or expired chunks.",
    seeAlso: ["batch", "amount", "batch-depth"],
  },
  {
    id: "batch",
    term: "Postage batch",
    aliases: ["batch", "batches", "batch id", "batchid", "postage batch"],
    kid: "A book of stamps you buy once. It has a size (how many pieces) and an expiry date.",
    engineer:
      "Created on the postage contract with (amount per chunk, depth, bucket depth, immutable). POST /stamps/{amount}/{depth} buys one; GET /stamps lists usable, utilization and batchTTL. Usable after the node sees the creation event plus a few confirmations.",
    seeAlso: ["batch-depth", "amount", "ttl", "utilization"],
  },
  {
    id: "batch-depth",
    term: "Batch depth",
    aliases: ["batch depth"],
    kid: "How big your stamp book is. Each step up doubles how many pieces you can stamp.",
    engineer: "A batch can stamp up to 2^depth chunks (spread over 2^bucketDepth buckets). Cost = amount × 2^depth PLUR. Minimum depth is 17 on mainnet contracts.",
    seeAlso: ["bucket-depth", "dilute", "utilization"],
  },
  {
    id: "bucket-depth",
    term: "Bucket depth",
    aliases: ["bucket depth", "bucket", "buckets"],
    kid: "Your stamp book is split into pockets by address. If one pocket fills up, the book is 'full' even if others are empty.",
    engineer:
      "Chunks map to 2^bucketDepth buckets (16 on mainnet) by their address prefix; each bucket holds 2^(depth−bucketDepth) slots. Utilization = fullest bucket's count. GET /stamps/{id}/buckets shows the distribution.",
    seeAlso: ["utilization", "batch-depth"],
  },
  {
    id: "amount",
    term: "Amount (PLUR per chunk)",
    aliases: ["amount", "plur per chunk"],
    kid: "How much you prepay for each piece. More money = the pieces stay longer.",
    engineer:
      "Per-chunk balance in PLUR. The chain deducts currentPrice per block from every batch's normalised balance; TTL ≈ (amount − already-paid) / price × block time. Total cost = amount × 2^depth.",
    seeAlso: ["ttl", "chainstate", "bzz"],
  },
  {
    id: "bzz",
    term: "BZZ / PLUR",
    aliases: ["bzz", "plur", "xbzz", "sbzz"],
    kid: "BZZ is Swarm's money. PLUR is its tiniest coin: one BZZ is 10 million billion PLUR.",
    engineer: "BZZ is the ERC-20 token (16 decimals); PLUR is its base unit (1 BZZ = 10^16 PLUR). Stamps, SWAP cheques and stake are denominated in it. GET /wallet returns bzzBalance in PLUR.",
    seeAlso: ["amount", "chequebook"],
  },
  {
    id: "ttl",
    term: "TTL (time to live)",
    aliases: ["ttl", "time to live", "batch ttl"],
    kid: "How long until your stamps expire and the library may throw your pieces away.",
    engineer: "batchTTL in GET /stamps: seconds until the batch balance is used up at the current price. Price changes alter it; top-ups extend it, dilution halves it per depth step.",
    seeAlso: ["amount", "top-up", "dilute"],
  },
  {
    id: "immutable",
    term: "Immutable batch",
    aliases: ["immutable", "immutable batch", "immutableflag"],
    kid: "A stamp book that refuses new stamps when full instead of reusing old slots.",
    engineer:
      "Immutable: true at creation. When a bucket is full, mutable batches overwrite the oldest stamp index (the old chunk loses its claim); immutable batches reject the upload. Immutable batches cannot be diluted.",
    seeAlso: ["bucket-depth", "dilute"],
  },
  {
    id: "top-up",
    term: "Top-up",
    aliases: ["top up", "top-up", "topup"],
    kid: "Putting more money on your stamp book so it lasts longer.",
    engineer: "PATCH /stamps/topup/{id}/{amount}: adds amount PLUR per chunk (cost amount × 2^depth) to the batch on chain, extending TTL.",
    seeAlso: ["ttl", "amount"],
  },
  {
    id: "dilute",
    term: "Dilute",
    aliases: ["dilute", "dilution"],
    kid: "Make your stamp book bigger without paying more — but every page now lasts a shorter time.",
    engineer: "PATCH /stamps/dilute/{id}/{depth}: increases depth; capacity doubles and the per-chunk balance halves per step, so TTL halves. Not allowed for immutable batches.",
    seeAlso: ["batch-depth", "ttl", "immutable"],
  },
  {
    id: "utilization",
    term: "Utilization",
    aliases: ["utilization", "utilisation", "used"],
    kid: "How full the fullest pocket of your stamp book is.",
    engineer: "Max stamps issued in any bucket; capacity per bucket = 2^(depth−bucketDepth). At 100% a mutable batch starts overwriting, an immutable one rejects uploads.",
    seeAlso: ["bucket-depth", "immutable"],
  },
  {
    id: "usable",
    term: "Usable (batch)",
    aliases: ["usable"],
    kid: "Your stamp book has arrived and you can start using it.",
    engineer: "The node has seen the batch creation event and enough blocks passed (postage listener confirmations). Uploads with a non-usable batch fail.",
    seeAlso: ["batch", "chainstate"],
  },
  {
    id: "chainstate",
    term: "Chain state / current price",
    aliases: ["chainstate", "chain state", "current price", "price"],
    kid: "The price list for storage, read from the blockchain.",
    engineer: "GET /chainstate: block, chainTip, totalAmount and currentPrice (PLUR per chunk per block) as tracked by pkg/postage/listener from the postage and price-oracle contracts.",
    seeAlso: ["amount", "ttl"],
  },

  // ---- Incentives ----
  {
    id: "redistribution",
    term: "Redistribution",
    aliases: ["redistribution", "storage incentives", "round", "rounds"],
    kid: "A lottery that pays the neighborhoods that prove they really keep the data.",
    engineer:
      "pkg/storageincentives: each round (152 blocks by default) one neighborhood is selected by the anchor. Staked nodes there commit a reserve sample hash, reveal it, and one winner claims the pot. GET /redistributionstate shows phase, round and isFrozen.",
    seeAlso: ["phases", "stake", "neighborhood-selection"],
  },
  {
    id: "phases",
    term: "Commit / reveal / claim",
    aliases: ["commit", "reveal", "claim", "phase", "phases"],
    kid: "Three steps of the lottery: seal your answer, show it, then the winner collects.",
    engineer:
      "Round quarters: commit (submit obfuscated sample hash), reveal (disclose it with depth), claim (winner submits inclusion proofs and collects). Missing phases or disagreeing samples can freeze the node.",
    seeAlso: ["redistribution", "stake"],
  },
  {
    id: "stake",
    term: "Stake",
    aliases: ["stake", "staking"],
    kid: "A deposit you lock up to promise you'll behave. Cheaters lose part of it.",
    engineer: "pkg/storageincentives/staking: BZZ locked in the staking contract; required to play redistribution. Stake weights the win probability; freezing/slashing applies on misbehavior.",
    seeAlso: ["redistribution"],
  },
  {
    id: "neighborhood-selection",
    term: "Neighborhood selection",
    aliases: ["neighborhood selection", "selected", "anchor"],
    kid: "The lottery picks one group of neighbors each round.",
    engineer: "The round anchor (randomness from the previous round) selects the neighborhood whose prefix matches at the committed depth; only its nodes can commit that round.",
    seeAlso: ["redistribution", "neighborhood"],
  },
  {
    id: "salud",
    term: "Salud (health)",
    aliases: ["salud", "healthy", "unhealthy", "health"],
    kid: "Nodes check whether their neighbors look healthy, and ignore the sick ones for rewards.",
    engineer:
      "pkg/salud: periodically polls peers' /status and marks peers unhealthy when their radius or reserve differs from the neighborhood consensus or responses are slow. A node that thinks itself unhealthy skips redistribution.",
    seeAlso: ["storage-radius", "redistribution"],
  },

  // ---- Accounting ----
  {
    id: "accounting",
    term: "Accounting (debt / overdraw)",
    aliases: ["accounting", "debt", "overdraw", "disconnect threshold", "payment threshold"],
    kid: "Nodes keep a tab of favors. If someone asks for too many favors without paying, they get cut off for a while.",
    engineer:
      "pkg/accounting: per-peer balances in accounting units for pushsync/retrieval. Crossing the payment threshold triggers settlement; crossing the disconnect threshold (overdraw) makes the creditor block the peer.",
    seeAlso: ["swap", "chequebook"],
  },
  {
    id: "swap",
    term: "SWAP / settlement",
    aliases: ["swap", "settlement", "settlements", "pseudosettle", "cheque", "cheques"],
    kid: "Paying friends back for their help, either with a little free allowance or with signed IOUs.",
    engineer: "pkg/settlement: pseudosettle (time-based free allowance) and swap (signed cheques from the chequebook, cashed on chain). /settlements and /chequebook/cheque show state.",
    seeAlso: ["accounting", "chequebook"],
  },
  {
    id: "chequebook",
    term: "Chequebook",
    aliases: ["chequebook", "checkbook"],
    kid: "The node's bank account that it writes IOUs from.",
    engineer: "A per-node contract (pkg/settlement/swap/chequebook) funded with BZZ; SWAP cheques draw on it. GET /chequebook/balance reports total and available balance.",
    seeAlso: ["swap", "bzz"],
  },

  // ---- Node states ----
  {
    id: "phase-up",
    term: "Up",
    aliases: ["up"],
    kid: "The node answers and is working.",
    engineer: "bee-scope phase: /health reachable and /status reports a usable state. Derived from polls through the kube service proxy.",
    seeAlso: ["phase-gated", "phase-unreachable"],
  },
  {
    id: "phase-gated",
    term: "Gated",
    aliases: ["gated"],
    kid: "The node is alive but not ready to take part yet, like a shop still opening.",
    engineer: "bee-scope phase: the API responds but /readiness or /status says the node is not yet ready (e.g. syncing postage, warming up).",
    seeAlso: ["phase-up"],
  },
  {
    id: "phase-unreachable",
    term: "Unreachable",
    aliases: ["unreachable"],
    kid: "We can't talk to the node right now.",
    engineer: "bee-scope phase: polls through the kube API service proxy fail (pod down, restarting, no endpoints, or the API server is unreachable).",
    seeAlso: ["statefulset", "phase-up"],
  },

  // ---- Local tooling ----
  {
    id: "geth",
    term: "geth (local chain)",
    aliases: ["geth", "geth-swap", "local chain", "blockchain"],
    kid: "A pretend blockchain on your computer, so nodes can buy stamps without real money.",
    engineer:
      "The geth-swap helm chart (ethersphere/helm) runs a dev chain; the setupcontracts job deploys BZZ, postage, staking, redistribution and price-oracle contracts. Bee nodes use it as --blockchain-rpc-endpoint.",
    seeAlso: ["beelocal", "chainstate"],
  },
  {
    id: "beekeeper",
    term: "beekeeper",
    aliases: ["beekeeper"],
    kid: "The robot that sets up Bee nodes and tests them.",
    engineer:
      "ethersphere/beekeeper: creates Bee clusters on Kubernetes from YAML (clusters → node groups → bee configs), funds nodes, and runs checks (ci-pushsync, ci-retrieval, …). bee-scope generates a 'bee-scope' cluster from a profile and calls create bee-cluster / check.",
    seeAlso: ["profile", "beelocal", "k3d"],
  },
  {
    id: "beelocal",
    term: "beelocal",
    aliases: ["beelocal"],
    kid: "The recipe that builds the little practice world (cluster + blockchain) on your laptop.",
    engineer: "ethersphere/beelocal: beelocal.sh creates the k3d cluster from config/k3d.yaml, a local registry, Traefik, and geth-swap. bee-scope runs the same steps with explicit contexts.",
    seeAlso: ["k3d", "geth", "beekeeper"],
  },
  {
    id: "k3d",
    term: "k3d cluster",
    aliases: ["k3d", "cluster", "k3s", "kubernetes"],
    kid: "A mini data-center that runs inside Docker on your computer.",
    engineer: "k3d runs k3s (lightweight Kubernetes) in Docker containers. Context k3d-bee, namespace local; bee-scope refuses non-k3d / non-loopback contexts.",
    seeAlso: ["statefulset", "beelocal"],
  },
  {
    id: "statefulset",
    term: "StatefulSet / pod",
    aliases: ["statefulset", "pod", "pods", "replicas"],
    kid: "Kubernetes' way of saying 'keep one copy of this node running, and restart it if it falls over'.",
    engineer:
      "beekeeper creates one StatefulSet per Bee node (name = node, label app.kubernetes.io/instance), with services, ingress route, keys secret and config map. Restart = delete the pod; stop = scale to 0.",
    seeAlso: ["k3d"],
  },
  {
    id: "profile",
    term: "Profile (beekeeper cluster)",
    aliases: ["profile", "profiles"],
    kid: "A saved recipe: how many nodes of each kind to create.",
    engineer: "A cluster definition in the beekeeper config dir that targets namespace local (e.g. local, local-dns). Its node groups and counts seed the deploy form.",
    seeAlso: ["beekeeper"],
  },

  // ---- bee-scope concepts ----
  {
    id: "provenance",
    term: "Provenance",
    aliases: ["provenance", "observed", "derived", "inferred", "simulated", "prov"],
    kid: "Where a fact comes from: seen directly, worked out, guessed, or made up for practice.",
    engineer:
      "bee-scope labels every value: observed (one source said so), derived (computed from observations, e.g. rates), inferred (a hypothesis from rules), simulated (--sim). Never mixed silently.",
    seeAlso: ["issue-class"],
  },
  {
    id: "issue-class",
    term: "Issue class",
    aliases: ["confirmed", "suspicious", "hypothesis", "issue", "issues", "anomaly"],
    kid: "How sure we are that something is wrong: sure, probably, or just a hunch.",
    engineer:
      "bee-scope anomaly rules: confirmed = directly observed (e.g. node unreachable); suspicious = likely, check the evidence; hypothesis = statistical outlier. Each issue lists evidence, a hypothesis and next steps.",
    seeAlso: ["provenance"],
  },
  {
    id: "flows",
    term: "Flow particles",
    aliases: ["flow", "flows", "particles"],
    kid: "The moving dots show real messages traveling between nodes.",
    engineer: "Particles are drawn only for per-peer events observed in logs or spans that name a peer (pushsync, retrieval, …). Metric-only activity is shown as pulses, never as particles.",
    seeAlso: ["provenance"],
  },
  {
    id: "keyframe",
    term: "Keyframe / time cursor",
    aliases: ["keyframe", "time cursor", "cursor", "scrub", "history"],
    kid: "Rewind the movie to see what the cluster looked like a few minutes ago.",
    engineer: "bee-scope stores a full snapshot every 5 s plus all events in bounded rings. Scrubbing the history strip loads the nearest keyframe; Live returns to the stream.",
    seeAlso: ["provenance"],
  },
];

const byId = new Map(glossary.map((g) => [g.id, g]));

export function entry(id: string): GlossaryEntry | undefined {
  return byId.get(id);
}

/** Lowercased term/alias → entry id, longest first so "storage radius" beats "radius". */
const keys: [string, string][] = glossary
  .flatMap((g) => [g.term, ...(g.aliases ?? [])].map((k) => [k.toLowerCase().replace(/\s*\(.*\)$/, ""), g.id] as [string, string]))
  .sort((a, b) => b[0].length - a[0].length);

const esc = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

/** Finds the first glossary term in `text` (whole words, case-insensitive). */
export function match(text: string): GlossaryEntry | undefined {
  const t = text.toLowerCase().trim();
  if (!t || t.length > 400) return undefined;
  for (const [k, id] of keys) {
    if (t === k) return byId.get(id);
  }
  for (const [k, id] of keys) {
    if (k.length < 3) continue;
    if (new RegExp(`(^|[^a-z0-9-])${esc(k)}([^a-z0-9-]|$)`).test(t)) return byId.get(id);
  }
  return undefined;
}

/** Simple ranked search for the glossary browser and the ⌘K palette. */
export function search(q: string): GlossaryEntry[] {
  const s = q.toLowerCase().trim();
  if (!s) return glossary;
  const scored: [number, GlossaryEntry][] = [];
  for (const g of glossary) {
    const names = [g.term, ...(g.aliases ?? [])].map((x) => x.toLowerCase());
    let score = 0;
    if (names.some((n) => n === s)) score = 100;
    else if (names.some((n) => n.startsWith(s))) score = 60;
    else if (names.some((n) => n.includes(s))) score = 40;
    else if (g.kid.toLowerCase().includes(s) || g.engineer.toLowerCase().includes(s)) score = 10;
    if (score) scored.push([score, g]);
  }
  return scored.sort((a, b) => b[0] - a[0]).map((x) => x[1]);
}
