//! Rust side of `make retrieval-turboquant-benchmark` (#1137). Mirrors the
//! workload in `scripts/rust-port/cmd/turboquant-bench` and prints one JSON
//! object with raw per-operation latencies for the Go comparator.

use std::{env, hint::black_box, time::Instant};

use symdesk_index::turboquant::{Codec, unpack_sidecar_blob};

fn splitmix64(state: &mut u64) -> u64 {
    *state = state.wrapping_add(0x9e37_79b9_7f4a_7c15);
    let mut z = *state;
    z = (z ^ (z >> 30)).wrapping_mul(0xbf58_476d_1ce4_e5b9);
    z = (z ^ (z >> 27)).wrapping_mul(0x94d0_49bb_1331_11eb);
    z ^ (z >> 31)
}

fn floats32(n: usize, mut seed: u64) -> Vec<f32> {
    (0..n)
        .map(|_| (splitmix64(&mut seed) >> 40) as f32 / (1 << 24) as f32 * 2.0 - 1.0)
        .collect()
}

fn arg(name: &str) -> usize {
    let args: Vec<String> = env::args().collect();
    let at = args
        .iter()
        .position(|a| a == name)
        .unwrap_or_else(|| panic!("missing {name}"));
    args[at + 1]
        .parse()
        .unwrap_or_else(|_| panic!("invalid {name}"))
}

fn main() {
    let (dim, bit_width, vectors, queries, rounds) = (
        arg("--dim"),
        arg("--bit-width"),
        arg("--vectors"),
        arg("--queries"),
        arg("--rounds"),
    );
    let codec = Codec::new(dim as i64, bit_width as i64, 42, 0).expect("codec");
    let corpus: Vec<Vec<f32>> = (0..vectors).map(|i| floats32(dim, i as u64 + 1)).collect();
    let probes: Vec<Vec<f32>> = (0..queries)
        .map(|i| floats32(dim, 1_000_000 + i as u64))
        .collect();

    let mut encode_ns = Vec::with_capacity(vectors * rounds);
    let mut scan_ns = Vec::with_capacity(queries * rounds);
    let mut blobs = Vec::new();
    let mut best_sum = 0.0_f64;
    for round in 0..=rounds {
        blobs.clear();
        for vector in &corpus {
            let start = Instant::now();
            let (blob, _) = codec
                .encode_sidecar(black_box(vector), 0.0)
                .expect("encode");
            let elapsed = start.elapsed().as_nanos() as u64;
            if round > 0 {
                encode_ns.push(elapsed);
            }
            blobs.push(blob);
        }
        for query in &probes {
            // One quantized scan per query, like the Go DB path: unpack every
            // sidecar blob and score it.
            let start = Instant::now();
            let mut best = f32::MIN;
            for blob in &blobs {
                let code = unpack_sidecar_blob(blob).expect("unpack");
                best = best.max(codec.score(black_box(query), &code));
            }
            let elapsed = start.elapsed().as_nanos() as u64;
            if round > 0 {
                scan_ns.push(elapsed);
            }
            best_sum += f64::from(best);
        }
    }
    // Encode bytes are architecture-stable; scores may differ in the last
    // bit where Go fuses FMA, so they are compared with a tolerance.
    let mut checksum: u64 = 0;
    for blob in &blobs {
        for &b in blob {
            checksum = checksum.wrapping_mul(31).wrapping_add(u64::from(b));
        }
    }
    let list = |v: &[u64]| v.iter().map(u64::to_string).collect::<Vec<_>>().join(",");
    println!(
        "{{\"checksum\":{checksum},\"best_sum\":{best_sum},\"encode_ns\":[{}],\"scan_ns\":[{}]}}",
        list(&encode_ns),
        list(&scan_ns)
    );
}
