#![no_main]
#![deny(unsafe_code)]
use libfuzzer_sys::fuzz_target;
use symdesk_render::draw::animate::{AnimateOptions, animate_frames};
use symdesk_render::draw::builder::build_scene;
use symdesk_render::draw::facade::{RenderRequest, render};
use symdesk_render::draw::gif::emit_gif_scenes;
use symdesk_render::draw::png::RasterOptions;
use symdesk_render::draw::{Diagram, parse_json, parse_source};

/// Raster outputs allocate width*height RGBA like Go; cap the scene so the
/// target measures parser/emitter defects, not allocator limits.
const MAX_RASTER_PIXELS: f64 = 90_000.0;

/// Mermaid `A & B --> C & D` expands to the cross product and the layered
/// layout splits every long edge into per-layer segments, so a 2 KiB input
/// can ask for 26k edges / 850k segments (35 s in a plain release build; Go's
/// layout is slower still, there is no limit in the Go contract). The parser
/// and serializer always see the input; layout and emitters only see
/// diagrams within these bounds, so the target measures defects rather than
/// the known quadratic cost.
const MAX_LAYOUT_NODES: usize = 128;
const MAX_LAYOUT_EDGES: usize = 256;

fn exercise(diagram: &Diagram) {
    // Traverse serialization too, including arbitrary custom JSON values.
    let _ = diagram.to_json();
    if diagram.nodes.len() > MAX_LAYOUT_NODES || diagram.edges.len() > MAX_LAYOUT_EDGES {
        return;
    }
    let Ok(scene) = build_scene(diagram) else {
        return;
    };
    // Facade outputs reuse the built scene: layout dominates the cost.
    for format in ["svg", "typst"] {
        let _ = render(RenderRequest {
            scene: Some(scene.clone()),
            format: format.to_owned(),
            ..Default::default()
        });
    }
    let raster_ok = scene.width.is_finite()
        && scene.height.is_finite()
        && scene.width > 0.0
        && scene.height > 0.0
        && scene.width * scene.height <= MAX_RASTER_PIXELS;
    if !raster_ok {
        return;
    }
    let _ = render(RenderRequest {
        scene: Some(scene.clone()),
        format: "png".to_owned(),
        ..Default::default()
    });
    if let Ok(frames) = animate_frames(
        Some(&scene),
        &AnimateOptions {
            frames: 3,
            steps: Vec::new(),
        },
    ) {
        let _ = emit_gif_scenes(&frames, 10, &RasterOptions { scale: 1.0 });
    }
}

fuzz_target!(|data: &[u8]| {
    if let Ok(diagram) = parse_json(data) {
        exercise(&diagram);
    }
    if let Ok(source) = std::str::from_utf8(data)
        && let Ok(diagram) = parse_source(source)
    {
        exercise(&diagram);
    }
});
