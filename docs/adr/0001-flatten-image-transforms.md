# ADR-0001: Flatten current composition for image transforms

Status: Accepted
Date: 2026-09-12

## Context

The editor retains a captured bitmap, independent annotations, a reversible crop, and an output-border setting. Rotating annotation coordinates individually would require defining rotation semantics for text, mosaic blocks, stroke widths, and crop expansion. The requested conservative behavior transforms the entire currently edited image.

## Decision

Rotate/flip the visible crop of the rendered bitmap and all annotations into a new immutable editing base. Clear the annotations and reset the crop to the result. Retain the output-border setting and apply it after transformation rather than baking it into the pixels. Preserve the prior complete editor state through the existing bounded undo history.

Keep drag angles and GPU preview transforms in the UI. Do not resample or create history entries during dragging. On release, inverse-map the composition once, using exact pixel relocation for quarter turns/flips and premultiplied-alpha bilinear interpolation for other integer angles.

## Alternatives and consequences

Keeping a non-destructive scene transform could preserve individually editable annotations but would require all rendering, hit-testing, cropping, and export paths to understand transformed layers. Transforming each annotation independently would risk divergent text/mosaic behavior. Both alternatives exceed this change's conservative scope.

The chosen behavior is predictable across save and copy, but flattening removes individual annotation editing and crop re-expansion until undo. Repeated arbitrary rotations resample pixels and expand transparent bounds. Output size is limited to 64 million pixels; the existing 20-step history can retain several large immutable bitmaps. The UI explains flattening before use. Capture-time original files remain unchanged.
