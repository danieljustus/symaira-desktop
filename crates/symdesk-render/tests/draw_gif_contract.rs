//! Replays `testdata/port/render/gif.json` (recorded from the production Go
//! `draw.AnimateFrames` + `emit.EmitGIF`) against the Rust port: every case
//! must produce the identical GIF byte stream (SHA-256, logical screen size,
//! frame count) or the identical error text.

use sha2::{Digest, Sha256};
use std::collections::HashMap;
use std::sync::atomic::{AtomicUsize, Ordering};
use symdesk_render::draw::{
    animate::{AnimateOptions, animate_frames},
    builder::build_scene,
    decode_unvalidated,
    gif::{dither_frame, emit_gif, emit_gif_scenes, encode_all},
    parse_source,
    png::{RasterOptions, rasterize},
    scene::{Primitive, Scene},
};

const FIXTURE: &str = include_str!("../../../testdata/port/render/gif.json");
const EMIT: &str = include_str!("../../../testdata/port/render/emit.json");

fn sha(bytes: &[u8]) -> String {
    format!("{:x}", Sha256::digest(bytes))
}

fn sources() -> HashMap<String, (String, String)> {
    let emit: serde_json::Value = serde_json::from_str(EMIT).unwrap();
    emit["cases"]
        .as_array()
        .unwrap()
        .iter()
        .map(|c| {
            (
                c["id"].as_str().unwrap().to_owned(),
                (
                    c["mode"].as_str().unwrap().to_owned(),
                    c["input"].as_str().unwrap().to_owned(),
                ),
            )
        })
        .collect()
}

fn scene_for(mode: &str, input: &str) -> Result<Scene, String> {
    let diagram = if mode == "ir" {
        decode_unvalidated(input.as_bytes()).unwrap()
    } else {
        parse_source(input).unwrap()
    };
    build_scene(&diagram)
}

fn ref_scene(srcs: &HashMap<String, (String, String)>, id: &str) -> Scene {
    let (mode, input) = &srcs[id];
    scene_for(mode, input).unwrap()
}

fn gif_dims(gif: &[u8]) -> (u64, u64) {
    assert_eq!(&gif[..6], b"GIF89a");
    (
        u64::from(u16::from_le_bytes([gif[6], gif[7]])),
        u64::from(u16::from_le_bytes([gif[8], gif[9]])),
    )
}

fn count_frames(gif: &[u8]) -> usize {
    // Each frame carries exactly one image descriptor; walk the block
    // structure instead of searching for the 0x2c byte.
    let mut i = 13; // header (6) + logical screen descriptor (7), no global table
    let mut frames = 0;
    loop {
        match gif[i] {
            0x3b => return frames,
            0x21 => {
                i += 2;
                while gif[i] != 0 {
                    i += 1 + gif[i] as usize;
                }
                i += 1;
            }
            0x2c => {
                frames += 1;
                let flags = gif[i + 9];
                i += 10;
                if flags & 0x80 != 0 {
                    i += 3 << ((flags & 7) + 1);
                }
                i += 1; // LZW minimum code size
                while gif[i] != 0 {
                    i += 1 + gif[i] as usize;
                }
                i += 1;
            }
            other => panic!("unexpected GIF block {other:#x}"),
        }
    }
}

fn animate(sc: &Scene, c: &serde_json::Value) -> Result<Vec<Scene>, String> {
    let steps = c["anim_steps"]
        .as_array()
        .map(|rows| {
            rows.iter()
                .map(|r| {
                    r.as_array()
                        .unwrap()
                        .iter()
                        .map(|v| v.as_i64().unwrap())
                        .collect()
                })
                .collect()
        })
        .unwrap_or_default();
    animate_frames(
        Some(sc),
        &AnimateOptions {
            frames: c["anim_frames"].as_i64().unwrap(),
            steps,
        },
    )
}

fn replay(srcs: &HashMap<String, (String, String)>, c: &serde_json::Value, hashed: &AtomicUsize) {
    let id = c["id"].as_str().unwrap();
    let variant = c["variant"].as_str().unwrap();
    let want_err = c["error"].as_str().unwrap();
    let opts = RasterOptions {
        scale: c["scale"].as_f64().unwrap_or(0.0),
    };
    let delay = c["delay_cs"].as_i64().unwrap();

    let result: Result<(Vec<u8>, Vec<usize>), String> = match c["mode"].as_str().unwrap() {
        "synthetic" => synthetic(srcs, c, variant, delay, &opts),
        _ => {
            let (mode, input) = &srcs[c["ref"].as_str().unwrap()];
            assert_eq!(
                sha(input.as_bytes()),
                c["input_sha256"].as_str().unwrap(),
                "{id}: emit.json input drifted"
            );
            let sc = scene_for(mode, input).unwrap();
            let frames = if variant == "single" {
                Ok(vec![sc])
            } else {
                animate(&sc, c)
            };
            frames.and_then(|frames| {
                let counts = frames.iter().map(|f| f.primitives.len()).collect();
                emit_gif_scenes(&frames, delay, &opts).map(|b| (b, counts))
            })
        }
    };
    match result {
        Err(err) => {
            assert_eq!(err, want_err, "{id}");
            assert_eq!(c["sha256"], "", "{id}");
        }
        Ok((bytes, counts)) => {
            assert_eq!(want_err, "", "{id}");
            let want_counts: Vec<i64> = c["frame_primitives"]
                .as_array()
                .map(|a| a.iter().map(|v| v.as_i64().unwrap()).collect())
                .unwrap_or_default();
            let got_counts: Vec<i64> = counts.iter().map(|&n| n as i64).collect();
            if !want_counts.is_empty() {
                assert_eq!(got_counts, want_counts, "{id} frame primitives");
            }
            let (w, h) = gif_dims(&bytes);
            assert_eq!(w, c["width"].as_u64().unwrap(), "{id} width");
            assert_eq!(h, c["height"].as_u64().unwrap(), "{id} height");
            assert_eq!(
                count_frames(&bytes) as u64,
                c["frames"].as_u64().unwrap(),
                "{id} frames"
            );
            assert_eq!(sha(&bytes), c["sha256"].as_str().unwrap(), "{id}");
            hashed.fetch_add(1, Ordering::Relaxed);
        }
    }
}

fn synthetic(
    srcs: &HashMap<String, (String, String)>,
    c: &serde_json::Value,
    variant: &str,
    delay: i64,
    opts: &RasterOptions,
) -> Result<(Vec<u8>, Vec<usize>), String> {
    match variant {
        "animate-nil" => {
            return animate_frames(None, &AnimateOptions::default()).map(|_| unreachable!());
        }
        "animate-empty" => {
            let mut sc = ref_scene(srcs, "dsl-parse-ir-valid-graph");
            sc.width = 10.0;
            sc.height = 10.0;
            sc.primitives.clear();
            return animate_frames(Some(&sc), &AnimateOptions::default()).map(|_| unreachable!());
        }
        _ => {}
    }
    let a = ref_scene(srcs, c["ref"].as_str().unwrap());
    let b = ref_scene(srcs, c["ref2"].as_str().unwrap());
    let mut big = a.clone();
    big.width += 50.0;
    big.height += 50.0;
    let scenes: Vec<Option<&Scene>> = match variant {
        "no-scenes" => vec![],
        "nil-frame" => vec![Some(&a), None],
        "frame-grows" => vec![Some(&a), Some(&big)],
        "frame-shrinks" => vec![Some(&big), Some(&a)],
        "two-scenes" => vec![Some(&a), Some(&b)],
        other => panic!("unknown synthetic variant {other}"),
    };
    let counts = scenes
        .iter()
        .map(|s| s.map_or(0, |s| s.primitives.len()))
        .collect();
    emit_gif(&scenes, delay, opts).map(|bytes| (bytes, counts))
}

#[test]
fn go_gif_bytes_contract() {
    let fixture: serde_json::Value = serde_json::from_str(FIXTURE).unwrap();
    assert_eq!(fixture["schema_version"], 1);
    let cases = fixture["cases"].as_array().unwrap();
    assert_eq!(cases.len(), 358);
    let srcs = sources();
    let next = AtomicUsize::new(0);
    let hashed = AtomicUsize::new(0);
    let workers = std::thread::available_parallelism().map_or(2, |n| n.get());
    std::thread::scope(|scope| {
        for _ in 0..workers {
            scope.spawn(|| {
                loop {
                    let i = next.fetch_add(1, Ordering::Relaxed);
                    let Some(c) = cases.get(i) else { break };
                    replay(&srcs, c, &hashed);
                }
            });
        }
    });
    // 358 cases: 5 recorded rejections (no scenes, nil frame, frame growth,
    // AnimateFrames nil and empty), 353 recorded GIFs.
    assert_eq!(hashed.load(Ordering::Relaxed), 353);
}

/// Mutation control: one dithered pixel and one encoded byte must both leave
/// the recorded hash.
#[test]
fn gif_hash_detects_one_pixel_and_one_byte_changes() {
    let fixture: serde_json::Value = serde_json::from_str(FIXTURE).unwrap();
    let srcs = sources();
    let case = fixture["cases"]
        .as_array()
        .unwrap()
        .iter()
        .find(|c| c["variant"] == "single" && !c["sha256"].as_str().unwrap().is_empty())
        .unwrap();
    let want = case["sha256"].as_str().unwrap();
    let (mode, input) = &srcs[case["ref"].as_str().unwrap()];
    let sc = scene_for(mode, input).unwrap();
    // `EmitGIF` replaces a non-positive delay with 50 cs.
    let delay = case["delay_cs"].as_i64().unwrap().max(50);
    let opts = RasterOptions::default();

    let rgba = rasterize(Some(&sc), &opts).unwrap();
    let frame = dither_frame(&rgba);
    assert_eq!(
        sha(&encode_all(std::slice::from_ref(&frame), delay).unwrap()),
        want,
        "unmutated control"
    );

    let mut pixel = dither_frame(&rgba);
    let mid = pixel.height / 2 * pixel.width + pixel.width / 2;
    pixel.pix[mid] ^= 1;
    assert_ne!(
        sha(&encode_all(&[pixel], delay).unwrap()),
        want,
        "one-pixel change"
    );

    let mut bytes = emit_gif_scenes(std::slice::from_ref(&sc), delay, &opts).unwrap();
    assert_eq!(sha(&bytes), want);
    let last = bytes.len() / 2;
    bytes[last] ^= 1;
    assert_ne!(sha(&bytes), want, "one-byte change");

    // A hidden primitive changes the animation frames and so the hash.
    let mut fewer = sc.clone();
    fewer
        .primitives
        .retain(|p| !matches!(p, Primitive::Text(_)));
    if fewer.primitives.len() != sc.primitives.len() {
        let b = emit_gif_scenes(&[fewer], delay, &opts).unwrap();
        assert_ne!(sha(&b), want, "removed primitive");
    }
}
