//! Regressions for `cargo fuzz run draw_parser` findings: ~1 KiB wide
//! flowcharts kept the layered layout busy for tens of seconds under the
//! fuzzer's instrumentation (1.2 s in a plain release build). Two hot spots:
//! the transposition pass recounted every crossing through string-keyed hash
//! maps for each candidate swap, and the median ordering rescanned all
//! segments for every node of a layer. Both now run on adjacency/interned
//! indices; output stays byte-identical to Go (`draw_layout_contract`).

use std::time::{Duration, Instant};
use symdesk_render::draw::{builder::build_scene, parse_source};

const TRANSPOSE: &str = include_str!("data/layout-transpose-timeout.mmd");
const MEDIAN: &str = include_str!("data/layout-median-timeout.mmd");

fn layout_time(source: &str) -> Duration {
    let diagram = parse_source(source).expect("fuzz input parses");
    let start = Instant::now();
    let scene = build_scene(&diagram).expect("scene builds");
    let elapsed = start.elapsed();
    assert!(!scene.primitives.is_empty());
    elapsed
}

#[test]
fn wide_flowcharts_lay_out_quickly() {
    // Pre-fix release builds needed >1 s per input and the instrumented
    // fuzzer 13-32 s; allow a slow shared debug host.
    for (name, source) in [("transpose", TRANSPOSE), ("median", MEDIAN)] {
        let elapsed = layout_time(source);
        eprintln!("layout of the {name} fuzz input took {elapsed:?}");
        assert!(elapsed < Duration::from_secs(10), "{name}: {elapsed:?}");
    }
}
