//! Replays `testdata/port/render/png.json` (recorded from the production Go
//! `emit.EmitPNG`) against the Rust PNG emitter: every non-withheld case must
//! produce the identical PNG byte stream (SHA-256 and IHDR size).

use sha2::{Digest, Sha256};
use std::sync::atomic::{AtomicUsize, Ordering};
use symdesk_render::draw::{
    builder::build_scene,
    decode_unvalidated, parse_source,
    png::{RasterOptions, emit_png, encode_png, rasterize},
    scene::Scene,
};

const FIXTURE: &str = include_str!("../../../testdata/port/render/png.json");

fn scene_for(mode: &str, input: &str) -> Result<Scene, String> {
    let diagram = if mode == "ir" {
        decode_unvalidated(input.as_bytes()).unwrap()
    } else {
        parse_source(input).unwrap()
    };
    build_scene(&diagram)
}

/// The generator's capture budget (`renderpnggen`): oversized scenes are
/// withheld instead of rasterized.
fn over_budget(sc: &Scene) -> bool {
    let mut w = sc.width.ceil();
    let mut h = sc.height.ceil();
    if w <= 0.0 {
        w = 100.0;
    }
    if h <= 0.0 {
        h = 100.0;
    }
    w.is_nan() || h.is_nan() || w > 4096.0 || h > 4096.0 || w * h > 4_000_000.0
}

fn sha(bytes: &[u8]) -> String {
    hex::encode(Sha256::digest(bytes))
}

#[test]
fn go_png_bytes_contract() {
    let fixture: serde_json::Value = serde_json::from_str(FIXTURE).unwrap();
    assert_eq!(fixture["schema_version"], 1);
    let cases = fixture["cases"].as_array().unwrap();
    assert_eq!(cases.len(), 1346);
    let next = AtomicUsize::new(0);
    let hashed = AtomicUsize::new(0);
    let errors = AtomicUsize::new(0);
    let withheld = AtomicUsize::new(0);
    // Rasterizing 1273 scenes is the slow part of a debug build: fan the
    // cases out over the available cores (each case is independent).
    let workers = std::thread::available_parallelism().map_or(2, |n| n.get());
    std::thread::scope(|scope| {
        for _ in 0..workers {
            scope.spawn(|| {
                loop {
                    let i = next.fetch_add(1, Ordering::Relaxed);
                    let Some(c) = cases.get(i) else { break };
                    replay(c, &hashed, &errors, &withheld);
                }
            });
        }
    });
    assert_eq!(
        (
            hashed.load(Ordering::Relaxed),
            errors.load(Ordering::Relaxed),
            withheld.load(Ordering::Relaxed)
        ),
        (1273, 49, 24)
    );
}

fn replay(
    c: &serde_json::Value,
    hashed: &AtomicUsize,
    errors: &AtomicUsize,
    withheld: &AtomicUsize,
) {
    let id = c["id"].as_str().unwrap();
    let sc = match scene_for(c["mode"].as_str().unwrap(), c["input"].as_str().unwrap()) {
        Ok(sc) => sc,
        Err(err) => {
            assert_eq!(err, c["error"].as_str().unwrap(), "{id}");
            assert_eq!(c["sha256"], "", "{id}");
            errors.fetch_add(1, Ordering::Relaxed);
            return;
        }
    };
    assert_eq!(c["error"], "", "{id}");
    if !c["withheld"].as_str().unwrap().is_empty() {
        assert!(over_budget(&sc), "{id}: withheld but within budget");
        withheld.fetch_add(1, Ordering::Relaxed);
        return;
    }
    assert!(!over_budget(&sc), "{id}: over budget but recorded");
    let png = emit_png(Some(&sc), &RasterOptions::default()).unwrap();
    assert_eq!(&png[..8], b"\x89PNG\r\n\x1a\n", "{id}");
    let width = u32::from_be_bytes(png[16..20].try_into().unwrap());
    let height = u32::from_be_bytes(png[20..24].try_into().unwrap());
    assert_eq!(u64::from(width), c["width"].as_u64().unwrap(), "{id} width");
    assert_eq!(
        u64::from(height),
        c["height"].as_u64().unwrap(),
        "{id} height"
    );
    assert_eq!(sha(&png), c["sha256"].as_str().unwrap(), "{id}");
    hashed.fetch_add(1, Ordering::Relaxed);
}

/// Mutation control: a one-pixel change in the raster, and a one-byte change
/// in the encoded stream, must both leave the recorded hash.
#[test]
fn png_hash_detects_one_pixel_and_one_byte_changes() {
    let fixture: serde_json::Value = serde_json::from_str(FIXTURE).unwrap();
    let case = fixture["cases"]
        .as_array()
        .unwrap()
        .iter()
        .find(|c| !c["sha256"].as_str().unwrap().is_empty())
        .unwrap();
    let want = case["sha256"].as_str().unwrap();
    let sc = scene_for(
        case["mode"].as_str().unwrap(),
        case["input"].as_str().unwrap(),
    )
    .unwrap();

    let img = rasterize(Some(&sc), &RasterOptions::default()).unwrap();
    assert_eq!(sha(&encode_png(&img).unwrap()), want, "unmutated control");

    let mut pixel = rasterize(Some(&sc), &RasterOptions::default()).unwrap();
    let mid = (pixel.height / 2 * pixel.width + pixel.width / 2) * 4;
    pixel.pix[mid] ^= 1;
    assert_ne!(sha(&encode_png(&pixel).unwrap()), want, "one-pixel change");

    let mut bytes = emit_png(Some(&sc), &RasterOptions::default()).unwrap();
    let last = bytes.len() / 2;
    bytes[last] ^= 1;
    assert_ne!(sha(&bytes), want, "one-byte change");
}

#[test]
fn png_nil_scene_contract() {
    assert_eq!(
        emit_png(None, &RasterOptions::default()).unwrap_err(),
        "scene is nil"
    );
}
