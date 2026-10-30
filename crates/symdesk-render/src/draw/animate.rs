//! Port of `internal/draw/animate.go`: step-sequence animation over one
//! diagram scene (appear animation), feeding [`super::gif::emit_gif_scenes`].

use super::scene::Scene;

/// Go `draw.AnimateOptions`.
#[derive(Debug, Clone, Default)]
pub struct AnimateOptions {
    /// Number of frames; `<= 0` selects one frame per primitive.
    pub frames: i64,
    /// Optional step groups: step `i` reveals the listed primitive indices
    /// in frame `i`. Empty distributes the primitives evenly.
    pub steps: Vec<Vec<i64>>,
}

/// Go `draw.AnimateFrames`.
pub fn animate_frames(sc: Option<&Scene>, opts: &AnimateOptions) -> Result<Vec<Scene>, String> {
    let sc = sc.ok_or_else(|| "scene is nil".to_owned())?;
    let count = sc.primitives.len();
    if count == 0 {
        return Err("scene has no primitives to animate".to_owned());
    }
    let frames = if opts.frames <= 0 {
        count
    } else {
        opts.frames as usize
    };

    let mut visible = vec![vec![false; count]; frames];
    if !opts.steps.is_empty() {
        for (step_idx, step) in opts.steps.iter().enumerate() {
            if step_idx >= frames {
                break;
            }
            for &p in step {
                if p >= 0 && (p as usize) < count {
                    visible[step_idx][p as usize] = true;
                }
            }
        }
        // A primitive revealed in an earlier step stays visible later.
        for f in 1..frames {
            for p in 0..count {
                if visible[f - 1][p] {
                    visible[f][p] = true;
                }
            }
        }
    } else {
        for p in 0..count {
            let mut frame = p * frames / count;
            if frame >= frames {
                frame = frames - 1;
            }
            for row in visible.iter_mut().skip(frame) {
                row[p] = true;
            }
        }
    }

    let mut out = Vec::with_capacity(frames);
    for row in &visible {
        let mut frame = sc.clone();
        frame.primitives = sc
            .primitives
            .iter()
            .zip(row)
            .filter(|(_, v)| **v)
            .map(|(p, _)| p.clone())
            .collect();
        out.push(frame);
    }
    Ok(out)
}
