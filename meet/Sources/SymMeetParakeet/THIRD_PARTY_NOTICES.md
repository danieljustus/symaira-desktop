# Third-party notices

## FluidAudio 0.18.3 (Apache-2.0)

`SymMeetParakeet` uses the `FluidAudio` product from
[FluidInference/FluidAudio](https://github.com/FluidInference/FluidAudio), pinned to
version 0.18.3 (revision `c8b41f98eae668794dd0083bbe028fe7e6585051`). FluidAudio is distributed
under the Apache License 2.0. See the package's `LICENSE` and `NOTICE` files for the complete
terms and the notices of its own dependencies.

## Parakeet Ultra speech model (CC-BY-4.0)

The speech recognition model is downloaded on first use by FluidAudio and is not part of the
app bundle:

- Core ML build: [FluidInference/parakeet-ultra-coreml](https://huggingface.co/FluidInference/parakeet-ultra-coreml)
- Post-training: [moondream/parakeet-ultra](https://huggingface.co/moondream/parakeet-ultra)
- Base model: [nvidia/parakeet-tdt-0.6b-v3](https://huggingface.co/nvidia/parakeet-tdt-0.6b-v3)
  by NVIDIA

The model and its derivatives are licensed under the Creative Commons Attribution 4.0
International License (CC-BY-4.0, <https://creativecommons.org/licenses/by/4.0/>). The Core ML
conversion and the Ultra post-training are modifications of the NVIDIA checkpoint.
