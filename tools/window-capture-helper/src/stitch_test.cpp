#define INSTANTCAPTURE_HELPER_TEST
#include "main.cpp"

#include <iostream>

namespace {

uint8_t sourceValue(int x, int y) {
    return static_cast<uint8_t>((x * 37 + y * 23 + (x * y) % 97) % 251);
}

Bitmap makeFrame(int width, int height, int offset, int stickyTop = 0, int stickyBottom = 0) {
    Bitmap bitmap;
    bitmap.width = width;
    bitmap.height = height;
    bitmap.bgra.resize(static_cast<size_t>(width) * height * 4);
    for (int y = 0; y < height; ++y) {
        for (int x = 0; x < width; ++x) {
            int contentY = offset + y;
            if (y < stickyTop) {
                contentY = 10000 + y;
            } else if (y >= height - stickyBottom) {
                contentY = 20000 + y;
            }
            uint8_t value = sourceValue(x, contentY);
            auto* pixel = bitmap.bgra.data() + (static_cast<size_t>(y) * width + x) * 4;
            pixel[0] = value;
            pixel[1] = static_cast<uint8_t>((value * 3) % 251);
            pixel[2] = static_cast<uint8_t>((value * 7) % 251);
            pixel[3] = 255;
        }
    }
    return bitmap;
}

Bitmap makeRepeatingFrame(int width, int height, int offset, int period) {
    Bitmap bitmap;
    bitmap.width = width;
    bitmap.height = height;
    bitmap.bgra.resize(static_cast<size_t>(width) * height * 4);
    for (int y = 0; y < height; ++y) {
        for (int x = 0; x < width; ++x) {
            uint8_t value = sourceValue(x, (y + offset) % period);
            auto* pixel = bitmap.bgra.data() + (static_cast<size_t>(y) * width + x) * 4;
            pixel[0] = value;
            pixel[1] = value;
            pixel[2] = value;
            pixel[3] = 255;
        }
    }
    return bitmap;
}

void paintFloatingOverlay(Bitmap& bitmap, int frameIndex, bool dynamicInterior) {
    int left = 17;
    int right = 73;
    int top = 43;
    int bottom = 68;
    for (int y = top; y < bottom; ++y) {
        for (int x = left; x < right; ++x) {
            bool border = x < left + 4 || x >= right - 4 || y < top + 4 || y >= bottom - 4;
            uint8_t value = static_cast<uint8_t>((x * 5 + y * 7 + 83) % 251);
            if (dynamicInterior && !border) {
                value = static_cast<uint8_t>((x * 11 + y * 3 + frameIndex * 47) % 251);
            }
            auto* pixel = bitmap.bgra.data() + (static_cast<size_t>(y) * bitmap.width + x) * 4;
            pixel[0] = value;
            pixel[1] = static_cast<uint8_t>((value * 2 + 31) % 251);
            pixel[2] = static_cast<uint8_t>((value * 5 + 17) % 251);
            pixel[3] = 255;
        }
    }
}

void paintFixedRect(Bitmap& bitmap, int left, int top, int right, int bottom, int seed) {
    for (int y = top; y < bottom; ++y) {
        for (int x = left; x < right; ++x) {
            uint8_t value = static_cast<uint8_t>((x * 7 + y * 11 + seed) % 251);
            auto* pixel = bitmap.bgra.data() + (static_cast<size_t>(y) * bitmap.width + x) * 4;
            pixel[0] = value;
            pixel[1] = static_cast<uint8_t>((value * 3 + 19) % 251);
            pixel[2] = static_cast<uint8_t>((value * 5 + 23) % 251);
            pixel[3] = 255;
        }
    }
}

void require(bool condition, char const* message);

void requireSourcePixels(Bitmap const& bitmap) {
    for (int y = 0; y < bitmap.height; ++y) {
        for (int x = 0; x < bitmap.width; ++x) {
            auto const* pixel = bitmap.bgra.data() + (static_cast<size_t>(y) * bitmap.width + x) * 4;
            if (pixel[0] != sourceValue(x, y)) {
                throw std::runtime_error("stitched body differs at " + std::to_string(x) + "," + std::to_string(y));
            }
        }
    }
}

void require(bool condition, char const* message) {
    if (!condition) {
        throw std::runtime_error(message);
    }
}

bool rowMatches(Bitmap const& bitmap, int leftY, int rightY) {
    auto const* left = bitmap.bgra.data() + static_cast<size_t>(leftY) * bitmap.width * 4;
    auto const* right = bitmap.bgra.data() + static_cast<size_t>(rightY) * bitmap.width * 4;
    return std::equal(left, left + static_cast<size_t>(bitmap.width) * 4, right);
}

void testExactVariableOverlap() {
    std::vector<Bitmap> frames{
        makeFrame(64, 120, 0),
        makeFrame(64, 120, 73),
        makeFrame(64, 120, 131),
    };
    Bitmap stitched = stitchVertical(frames, 48, {}, nullptr, true);
    require(stitched.width == 64 && stitched.height == 251, "variable-overlap dimensions differ");
    for (int y = 0; y < stitched.height; ++y) {
        for (int x = 0; x < stitched.width; ++x) {
            auto const* pixel = stitched.bgra.data() + (static_cast<size_t>(y) * stitched.width + x) * 4;
            require(pixel[0] == sourceValue(x, y), "variable-overlap pixels differ");
        }
    }
}

void testStickyEdgesAppearOnce() {
    std::vector<Bitmap> frames{
        makeFrame(64, 140, 0, 12, 10),
        makeFrame(64, 140, 70, 12, 10),
        makeFrame(64, 140, 140, 12, 10),
    };
    ViewportMask mask = detectViewportFixedMask(frames, {0, 70, 140});
    require(mask.at(8, 4), "sticky header was not detected");
    require(mask.at(8, 138), "sticky footer was not detected");
    int middleMask = 0;
    for (int x = 0; x < mask.width; ++x) {
        middleMask += mask.at(x, 69) ? 1 : 0;
    }
    require(middleMask == 0, ("body row mask count " + std::to_string(middleMask)).c_str());
    Bitmap stitched = stitchVertical(frames, 60, {}, nullptr, true);
    require(stitched.height == 280, "sticky-edge dimensions differ");
    int headerMatches = 0;
    int footerMatches = 0;
    for (int y = 0; y < stitched.height; ++y) {
        headerMatches += rowMatches(stitched, 0, y) ? 1 : 0;
        footerMatches += rowMatches(stitched, stitched.height - 1, y) ? 1 : 0;
    }
    require(headerMatches == 1, ("sticky header count " + std::to_string(headerMatches)).c_str());
    require(footerMatches == 1, ("sticky footer count " + std::to_string(footerMatches)).c_str());
}

void testStickyHeaderTallerThanMovementAppearsOnce() {
    std::vector<Bitmap> frames{
        makeFrame(96, 180, 0, 80, 0),
        makeFrame(96, 180, 30, 80, 0),
        makeFrame(96, 180, 60, 80, 0),
        makeFrame(96, 180, 90, 80, 0),
    };
    Bitmap stitched = stitchVertical(frames, 150, {-1, 150, 150, 150}, nullptr, true);
    require(stitched.height == 270, "thick sticky header dimensions differ");
    for (int y = 0; y < stitched.height; ++y) {
        for (int x = 0; x < stitched.width; ++x) {
            auto const* pixel = stitched.bgra.data() + (static_cast<size_t>(y) * stitched.width + x) * 4;
            if (y < 80) {
                auto const* expected = frames.front().bgra.data() + (static_cast<size_t>(y) * frames.front().width + x) * 4;
                require(std::equal(pixel, pixel + 4, expected), "thick sticky header was not preserved once");
            } else {
                require(pixel[0] == sourceValue(x, y), "thick sticky header damaged body content");
            }
        }
    }
}

void testThinStableBorderExpandsDynamicPanel() {
    std::vector<Bitmap> frames;
    std::vector<int> origins{0, 20, 40, 60};
    for (size_t frameIndex = 0; frameIndex < origins.size(); ++frameIndex) {
        Bitmap frame = makeFrame(96, 120, origins[frameIndex]);
        int left = 8;
        int right = 88;
        int top = 16;
        int bottom = 104;
        for (int y = top; y < bottom; ++y) {
            for (int x = left; x < right; ++x) {
                bool border = x < left + 2 || x >= right - 2 || y < top + 2 || y >= bottom - 2;
                uint8_t value = border
                    ? static_cast<uint8_t>((x * 9 + y * 5 + 41) % 251)
                    : static_cast<uint8_t>((x * 3 + y * 7 + static_cast<int>(frameIndex) * 53) % 251);
                auto* pixel = frame.bgra.data() + (static_cast<size_t>(y) * frame.width + x) * 4;
                pixel[0] = value;
                pixel[1] = value;
                pixel[2] = value;
                pixel[3] = 255;
            }
        }
        frames.push_back(std::move(frame));
    }
    ViewportMask mask = detectViewportFixedMask(frames, origins);
    require(mask.at(48, 60), "thin stable border did not expand over dynamic panel interior");
}

void testLShapedFixedChromeDoesNotMaskBoundingBox() {
    std::vector<Bitmap> frames;
    std::vector<int> origins{0, 30, 60};
    for (int origin : origins) {
        Bitmap frame = makeFrame(96, 120, origin);
        paintFixedRect(frame, 10, 20, 80, 24, 33);
        paintFixedRect(frame, 10, 20, 14, 90, 33);
        frames.push_back(std::move(frame));
    }
    ViewportMask mask = detectViewportFixedMask(frames, origins);
    require(mask.at(12, 40), "L-shaped fixed chrome was not detected");
    require(!mask.at(50, 60), "L-shaped fixed chrome masked its bounding box interior");
}

void testMiddleFloatingOverlayIsRemoved() {
    std::vector<Bitmap> frames;
    std::vector<int> origins{0, 30, 60, 90};
    for (size_t i = 0; i < origins.size(); ++i) {
        Bitmap frame = makeFrame(96, 120, origins[i]);
        paintFloatingOverlay(frame, static_cast<int>(i), false);
        frames.push_back(std::move(frame));
    }
    ViewportMask mask = detectViewportFixedMask(frames, origins);
    require(mask.at(30, 54), "middle floating overlay was not detected");
    int minimumMaskedY = mask.height;
    for (int y = 0; y < mask.height; ++y) {
        for (int x = 0; x < mask.width; ++x) {
            if (mask.at(x, y)) {
                minimumMaskedY = (std::min)(minimumMaskedY, y);
            }
        }
    }
    require(!mask.at(15, 29), ("floating overlay mask leaked into clean body; min y " + std::to_string(minimumMaskedY)).c_str());
    std::vector<int> measuredOrigins;
    Bitmap stitched;
    try {
        stitched = stitchVertical(frames, 90, {-1, 90, 90, 90}, &measuredOrigins, true);
    } catch (std::runtime_error const& err) {
        throw std::runtime_error("middle floating overlay: " + std::string(err.what()));
    }
    require(measuredOrigins == origins, "floating overlay changed document origins");
    require(stitched.width == 96 && stitched.height == 210, "floating-overlay dimensions differ");
    requireSourcePixels(stitched);
}

void testTwoFrameFloatingOverlayIsRemovedConservatively() {
    std::vector<Bitmap> frames;
    std::vector<int> origins{0, 30};
    for (size_t i = 0; i < origins.size(); ++i) {
        Bitmap frame = makeFrame(96, 120, origins[i]);
        paintFloatingOverlay(frame, static_cast<int>(i), false);
        frames.push_back(std::move(frame));
    }
    ViewportMask mask = detectViewportFixedMask(frames, origins);
    require(mask.at(30, 54), "two-frame floating overlay was not detected");
    Bitmap stitched = stitchVertical(frames, 90, {-1, 90}, nullptr, true);
    requireSourcePixels(stitched);
}

void testDynamicFixedInteriorIsRemovedWithStableBorder() {
    std::vector<Bitmap> frames;
    std::vector<int> origins{0, 30, 60, 90};
    for (size_t i = 0; i < origins.size(); ++i) {
        Bitmap frame = makeFrame(96, 120, origins[i]);
        paintFloatingOverlay(frame, static_cast<int>(i), true);
        frames.push_back(std::move(frame));
    }
    ViewportMask mask = detectViewportFixedMask(frames, origins);
    require(mask.at(30, 54), "dynamic fixed interior was not filled from stable component");
    Bitmap stitched;
    try {
        stitched = stitchVertical(frames, 90, {-1, 90, 90, 90}, nullptr, true);
    } catch (std::runtime_error const& err) {
        throw std::runtime_error("stable-border dynamic panel: " + std::string(err.what()));
    }
    requireSourcePixels(stitched);
}

void testBorderlessDynamicFixedPanelIsRemoved() {
    std::vector<Bitmap> frames;
    std::vector<int> origins{0, 30, 60, 90};
    for (size_t frameIndex = 0; frameIndex < origins.size(); ++frameIndex) {
        Bitmap frame = makeFrame(96, 120, origins[frameIndex]);
        for (int y = 43; y < 68; ++y) {
            for (int x = 17; x < 73; ++x) {
                uint8_t value = static_cast<uint8_t>((x * 11 + y * 7 + static_cast<int>(frameIndex) * 59) % 251);
                auto* pixel = frame.bgra.data() + (static_cast<size_t>(y) * frame.width + x) * 4;
                pixel[0] = value;
                pixel[1] = static_cast<uint8_t>((value * 3 + frameIndex * 17) % 251);
                pixel[2] = static_cast<uint8_t>((value * 5 + frameIndex * 29) % 251);
                pixel[3] = 255;
            }
        }
        frames.push_back(std::move(frame));
    }
    ViewportMask mask = detectViewportFixedMask(frames, origins);
    require(mask.at(30, 54), "borderless dynamic fixed panel was not detected");
    Bitmap stitched;
    try {
        stitched = stitchVertical(frames, 90, {-1, 90, 90, 90}, nullptr, true);
    } catch (std::runtime_error const& err) {
        throw std::runtime_error("borderless dynamic panel: " + std::string(err.what()));
    }
    requireSourcePixels(stitched);
}

void testTranslatedDynamicBodyIsNotFixed() {
    std::vector<Bitmap> frames;
    std::vector<int> origins{0, 30, 60, 90};
    for (size_t frameIndex = 0; frameIndex < origins.size(); ++frameIndex) {
        Bitmap frame = makeFrame(96, 120, origins[frameIndex]);
        for (int y = 0; y < frame.height; ++y) {
            int documentY = origins[frameIndex] + y;
            if (documentY < 95 || documentY >= 112) {
                continue;
            }
            for (int x = 24; x < 72; ++x) {
                uint8_t value = static_cast<uint8_t>((x * 13 + documentY * 3 + static_cast<int>(frameIndex) * 41) % 251);
                auto* pixel = frame.bgra.data() + (static_cast<size_t>(y) * frame.width + x) * 4;
                pixel[0] = value;
                pixel[1] = value;
                pixel[2] = value;
            }
        }
        frames.push_back(std::move(frame));
    }
    ViewportMask mask = detectViewportFixedMask(frames, origins);
    int fixedPixels = 0;
    for (uint8_t value : mask.fixed) {
        fixedPixels += value != 0 ? 1 : 0;
    }
    require(fixedPixels == 0, "translated dynamic body was classified as fixed");

    std::vector<int> measuredOrigins;
    Bitmap stitched;
    try {
        stitched = stitchVertical(frames, 90, {-1, 90, 90, 90}, &measuredOrigins, true);
    } catch (std::runtime_error const& err) {
        throw std::runtime_error("translated dynamic body: " + std::string(err.what()));
    }
    require(measuredOrigins == origins, "dynamic body changed document origins");
    require(stitched.width == 96 && stitched.height == 210, "dynamic body stitched dimensions differ");
    for (int y = 0; y < stitched.height; ++y) {
        for (int x = 0; x < stitched.width; ++x) {
            auto const* actual = stitched.bgra.data() + (static_cast<size_t>(y) * stitched.width + x) * 4;
            if (x >= 24 && x < 72 && y >= 95 && y < 112) {
                int owner = -1;
                int ownerRow = 0;
                int bestDistance = std::numeric_limits<int>::max();
                for (size_t frameIndex = 0; frameIndex < frames.size(); ++frameIndex) {
                    int sourceY = y - origins[frameIndex];
                    if (sourceY < 0 || sourceY >= frames[frameIndex].height) {
                        continue;
                    }
                    int distance = std::abs(sourceY - frames[frameIndex].height / 2);
                    if (distance < bestDistance) {
                        owner = static_cast<int>(frameIndex);
                        ownerRow = sourceY;
                        bestDistance = distance;
                    }
                }
                require(owner >= 0, "dynamic body owner frame was not found");
                auto const* expected = frames[static_cast<size_t>(owner)].bgra.data() + (static_cast<size_t>(ownerRow) * frames[static_cast<size_t>(owner)].width + x) * 4;
                require(std::equal(actual, actual + 4, expected), "dynamic body did not use deterministic center owner");
            } else {
                require(actual[0] == sourceValue(x, y), "dynamic body damaged adjacent static content");
            }
        }
    }
}

void testPersistentFixedSidebarUsesDeterministicFallback() {
    std::vector<Bitmap> frames;
    std::vector<int> origins{0, 30, 60, 90};
    for (int origin : origins) {
        Bitmap frame = makeFrame(96, 120, origin);
        paintFixedRect(frame, 0, 0, 20, 120, 193);
        frames.push_back(std::move(frame));
    }
    Bitmap stitched = stitchVertical(frames, 90, {-1, 90, 90, 90}, nullptr, true);
    Bitmap repeated = stitchVertical(frames, 90, {-1, 90, 90, 90}, nullptr, true);
    require(stitched.width == 96 && stitched.height == 210, "persistent fixed sidebar dimensions differ");
	require(stitched.bgra == repeated.bgra, "persistent fixed sidebar fallback was unstable");
    for (int y = 0; y < stitched.height; ++y) {
        auto const* body = stitched.bgra.data() + (static_cast<size_t>(y) * stitched.width + 40) * 4;
        require(body[0] == sourceValue(40, y), "persistent fixed sidebar damaged recoverable body content");
    }
}

void testSparseUnrecoverableMaskUsesFallback() {
    std::vector<Bitmap> frames{makeFrame(96, 120, 0), makeFrame(96, 120, 30)};
    ViewportMask mask;
    mask.width = 96;
    mask.height = 120;
    mask.fixed.assign(static_cast<size_t>(mask.width) * mask.height, 0);
    mask.fixed[static_cast<size_t>(50) * mask.width + 10] = 1;
    mask.fixed[static_cast<size_t>(20) * mask.width + 10] = 1;
    Bitmap stitched = composeDocumentFrames(frames, {0, 30}, mask);
    require(stitched.width == 96 && stitched.height == 150, "sparse fallback dimensions differ");
}

void testPartialFixedHeaderAndFooterAppearOnce() {
    std::vector<Bitmap> frames;
    std::vector<int> origins{0, 30, 60, 90};
    for (int origin : origins) {
        Bitmap frame = makeFrame(96, 120, origin);
        paintFixedRect(frame, 28, 0, 68, 16, 71);
        paintFixedRect(frame, 10, 108, 48, 120, 149);
        frames.push_back(std::move(frame));
    }
    ViewportMask mask = detectViewportFixedMask(frames, origins);
    require(mask.at(40, 8), "partial fixed header was not detected");
    require(mask.at(24, 114), "partial fixed footer was not detected");
    Bitmap stitched = stitchVertical(frames, 90, {-1, 90, 90, 90}, nullptr, true);
    require(stitched.height == 210, "partial fixed edge dimensions differ");
    for (int y = 0; y < stitched.height; ++y) {
        for (int x = 0; x < stitched.width; ++x) {
            auto const* pixel = stitched.bgra.data() + (static_cast<size_t>(y) * stitched.width + x) * 4;
            if (x >= 28 && x < 68 && y < 16) {
                auto const* expected = frames.front().bgra.data() + (static_cast<size_t>(y) * frames.front().width + x) * 4;
                require(std::equal(pixel, pixel + 4, expected), "partial header was not preserved from first frame");
            } else if (x >= 10 && x < 48 && y >= stitched.height - 12) {
                int sourceY = frames.back().height - (stitched.height - y);
                auto const* expected = frames.back().bgra.data() + (static_cast<size_t>(sourceY) * frames.back().width + x) * 4;
                require(std::equal(pixel, pixel + 4, expected), "partial footer was not preserved from last frame");
            } else {
                require(pixel[0] == sourceValue(x, y), "partial fixed edge damaged body content");
            }
        }
    }
}

void testTemporaryFloatingPopupIsRemovedByConsensus() {
    std::vector<Bitmap> frames;
    std::vector<int> origins{0, 30, 60, 90};
    for (size_t frameIndex = 0; frameIndex < origins.size(); ++frameIndex) {
        Bitmap frame = makeFrame(96, 120, origins[frameIndex]);
        if (frameIndex == 1 || frameIndex == 2) {
            paintFloatingOverlay(frame, static_cast<int>(frameIndex), false);
        }
        frames.push_back(std::move(frame));
    }
    Bitmap stitched = stitchVertical(frames, 90, {-1, 90, 90, 90}, nullptr, true);
    requireSourcePixels(stitched);
}

void testLongTemporaryPopupKeepsCleanEdgeFramesInConsensus() {
    std::vector<Bitmap> frames;
    std::vector<int> origins;
    std::vector<int> overlaps{-1};
    for (int frameIndex = 0; frameIndex < 12; ++frameIndex) {
        int origin = frameIndex * 10;
        origins.push_back(origin);
        if (frameIndex > 0) {
            overlaps.push_back(110);
        }
        Bitmap frame = makeFrame(96, 120, origin);
        if (frameIndex >= 2 && frameIndex <= 9) {
            paintFloatingOverlay(frame, frameIndex, false);
        }
        frames.push_back(std::move(frame));
    }
    Bitmap stitched = stitchVertical(frames, 110, overlaps, nullptr, true);
    requireSourcePixels(stitched);
}

void testRepeatingPatternIsRejected() {
    std::vector<Bitmap> frames{
        makeRepeatingFrame(64, 144, 0, 12),
        makeRepeatingFrame(64, 144, 7, 12),
    };
    bool rejected = false;
    try {
        (void)stitchVertical(frames, 48, {}, nullptr, true);
    } catch (std::runtime_error const&) {
        rejected = true;
    }
    require(rejected, "repeating overlap was accepted");
}

void testRepeatingPatternUsesMeasuredPrior() {
    std::vector<Bitmap> frames{
        makeRepeatingFrame(64, 144, 0, 12),
        makeRepeatingFrame(64, 144, 7, 12),
    };
    Bitmap stitched = stitchVertical(frames, 48, {-1, 137}, nullptr, true);
    require(stitched.height == 151, "measured prior did not resolve repeating overlap");
}

void testIncorrectPriorUsesVisualOverlap() {
    std::vector<Bitmap> frames{
        makeFrame(64, 120, 0),
        makeFrame(64, 120, 73),
    };
    Bitmap stitched = stitchVertical(frames, 48, {-1, 100}, nullptr, false);
    require(stitched.height == 193, "incorrect prior changed stitched height");
}

void testVariableRangeMovementUsesVisualOrigins() {
    std::vector<Bitmap> frames{
        makeFrame(96, 120, 0),
        makeFrame(96, 120, 20),
        makeFrame(96, 120, 60),
    };
    Options opt;
    opt.rangeSteps = 2;
    opt.rangeDistancePx = 60;
    std::vector<int> estimates = rangeOverlapEstimates(frames, {0, 1, 1}, opt);
    std::vector<int> origins;
    Bitmap stitched = stitchVertical(frames, 96, estimates, &origins, false);
    require(origins == std::vector<int>({0, 20, 60}), "range average overrode visual frame origins");
    require(stitched.height == 180, "variable range movement changed stitched extent");
    requireSourcePixels(stitched);
}

void testRangeCropUsesMeasuredFrameOrigins() {
    std::vector<Bitmap> frames{
        makeFrame(96, 120, 0),
        makeFrame(96, 120, 30),
        makeFrame(96, 120, 80),
    };
    Options opt;
    opt.rangeRequested = true;
    opt.rangeSteps = 2;
    opt.rangeDistancePx = 720;
    opt.rangeEndY = 100;
    opt.cropY = 20;
    opt.cropH = 800;
    int cropHeight = measuredRangeCropHeight({0, 30, 80}, {0, 1, 1}, frames, opt);
    require(cropHeight == 160, "range crop used estimated wheel distance instead of measured frame origins");
}

void testSelectionWidthCompositionMatchesFullDocumentCrop() {
    std::vector<Bitmap> frames{
        makeFrame(96, 120, 0, 12, 0),
        makeFrame(96, 120, 30, 12, 0),
        makeFrame(96, 120, 60, 12, 0),
    };
    Bitmap full = stitchVertical(frames, 90, {-1, 90, 90}, nullptr, true);
    Bitmap strip = stitchVertical(frames, 90, {-1, 90, 90}, nullptr, true, false, 24, 40);
    Bitmap expected = cropBitmap(full, 24, 0, 40, full.height);
    require(strip.width == 40 && strip.height == full.height, "selection-width composition dimensions differ");
    require(strip.bgra == expected.bgra, "selection-width composition differs from full-document crop");
}

void testCropRejectsOutOfBounds() {
    Bitmap bitmap = makeFrame(20, 20, 0);
    bool rejected = false;
    try {
        (void)cropBitmap(bitmap, 10, 10, 20, 20);
    } catch (std::runtime_error const&) {
        rejected = true;
    }
    require(rejected, "out-of-bounds crop was accepted");
}

void testDimensionChangeIsRejected() {
    std::vector<Bitmap> frames{makeFrame(64, 120, 0), makeFrame(63, 120, 70)};
    bool rejected = false;
    try {
        (void)stitchVertical(frames, 48);
    } catch (std::runtime_error const&) {
        rejected = true;
    }
    require(rejected, "dimension change was accepted");
}

void testRangeCoverageRequiresBottomSafety() {
    require(!rangeCoverageSatisfied(900, 900, 600, true), "exact bottom should require safety coverage");
    require(!rangeCoverageSatisfied(1019, 900, 600, true), "insufficient safety was accepted");
    require(rangeCoverageSatisfied(1020, 900, 600, true), "safety coverage was rejected");
    require(rangeCoverageSatisfied(900, 900, 600, false), "page-end exact coverage was rejected");
    require(!rangeCoverageSatisfied(899, 900, 600, false), "incomplete page-end coverage was accepted");
}

void testRangeStepBatchPreservesOverlap() {
    Options opt;
    opt.rangeSteps = 64;
    opt.rangeDistancePx = 6400;
    require(rangeStepBatch(opt, 1080, 64) == 2, "small movement batch differs");
    opt.rangeDistancePx = 23040;
    require(rangeStepBatch(opt, 1080, 64) == 2, "large movement batch differs");
    require(rangeStepBatch(opt, 1080, 1) == 1, "final batch differs");
    int sent = 0;
    for (int action = 0; action < 32 && sent < opt.rangeSteps; ++action) {
        sent += rangeStepBatch(opt, 1080, opt.rangeSteps - sent);
    }
    require(sent == 64, "33-frame plan cannot replay 64 steps");
    std::vector<int> inputs = rangeWheelInputs(2);
    require(inputs == std::vector<int>({-WHEEL_DELTA, -WHEEL_DELTA}), "batched range steps were collapsed into one wheel event");
    require(!rangeReplayComplete(1, 2), "partial range replay was accepted");
    require(rangeReplayComplete(2, 2), "complete range replay was rejected");
    WheelScrollGuard guard;
    guard.record(-WHEEL_DELTA);
    guard.record(-WHEEL_DELTA);
    guard.discardLast(2);
    require(guard.forwardDeltas.empty(), "ineffective batched inputs remained in restoration history");
}

void testStoredRangeFrameKeepsFullWidthEvidence() {
    Options opt;
    opt.rangeRequested = true;
    opt.cropX = 40;
    opt.cropW = 20;
    Bitmap frame = makeFrame(96, 120, 0);
    Bitmap stored = storedScrollFrame(opt, frame);
    require(stored.width == frame.width && stored.bgra == frame.bgra, "range frame was cropped before origin detection");
}

void testPartialWheelBatchRestoresOnlyMovedEvents() {
    int position = 100;
    Bitmap initial = makeFrame(96, 120, position);
    Bitmap moved = makeFrame(96, 120, 130);
    std::vector<int> restoreInputs;
    WheelScrollGuard guard;
    guard.hwnd = reinterpret_cast<HWND>(1);
    guard.originFrame = &initial;
    guard.overlapPx = 90;
    guard.captureViewport = [&]() { return makeFrame(96, 120, position); };
    guard.sendDelta = [&](int delta) {
        restoreInputs.push_back(delta);
        int movement = static_cast<int>(std::round(73.0 * std::abs(delta) / WHEEL_DELTA));
        position += delta > 0 ? -movement : movement;
        return true;
    };
    require(guard.recordIfMoved(-WHEEL_DELTA, initial, moved), "moving wheel event was not recorded");
    require(!guard.recordIfMoved(-WHEEL_DELTA, moved, moved), "stationary boundary wheel event was recorded");
    require(guard.forwardDeltas == std::vector<int>({-WHEEL_DELTA}), "partial batch retained an ineffective event");
    require(guard.pendingRestoreDeltas() == std::vector<int>({WHEEL_DELTA}), "partial batch restoration would overshoot the origin");
    position = 130;
    require(guard.restore(), "measured restoration failed after a partial boundary movement");
    require(position == 100, ("measured restoration ended at " + std::to_string(position)).c_str());
    require(restoreInputs == std::vector<int>({WHEEL_DELTA, -71}), "measured restoration did not correct reverse-notch overshoot");
    require(guard.captureViewport().bgra == initial.bgra, "restored viewport differs from the initial viewport");
}

void testStitchedOutputMemoryBudgetIsEnforced() {
    std::vector<Bitmap> frames{makeFrame(20, 20, 0), makeFrame(20, 20, 1)};
    ViewportMask mask;
    bool rejected = false;
    try {
        (void)composeDocumentFrames(frames, {0, 4'000'000}, mask);
    } catch (std::runtime_error const&) {
        rejected = true;
    }
    require(rejected, "oversized stitched output was allocated");
}

void testRangeParserRejectsMoreThanSixtyFourSteps() {
    char arg0[] = "helper";
    char arg1[] = "capture-scroll";
    char arg2[] = "--hwnd";
    char arg3[] = "1";
    char arg4[] = "--out";
    char arg5[] = "capture.png";
    char arg6[] = "--range-steps";
    char arg7[] = "65";
    char* argv[] = {arg0, arg1, arg2, arg3, arg4, arg5, arg6, arg7};
    bool rejected = false;
    try {
        (void)parseArgs(8, argv);
    } catch (std::runtime_error const&) {
        rejected = true;
    }
    require(rejected, "range parser silently clamped more than 64 steps");
}

void testScrollPositionCommandsDoNotRequireOutputPath() {
    char arg0[] = "helper";
    char query[] = "query-scroll";
    char hwnd[] = "--hwnd";
    char handle[] = "1";
    char json[] = "--json";
    char* queryArgv[] = {arg0, query, hwnd, handle, json};
    Options queryOptions = parseArgs(5, queryArgv);
    require(queryOptions.command == "query-scroll" && queryOptions.outputPath.empty(), "query-scroll required an output path");

    char restore[] = "restore-scroll";
    char percentName[] = "--scroll-percent";
    char percent[] = "37.5";
    char* restoreArgv[] = {arg0, restore, hwnd, handle, percentName, percent, json};
    Options restoreOptions = parseArgs(7, restoreArgv);
    require(std::abs(restoreOptions.scrollPercent - 37.5) < 0.001, "restore-scroll percent was not parsed");
}

void testFallbackOriginReplayUsesRecordedWheelUnits() {
    require(initialRestoreWheelInputs(300) == std::vector<int>({120, 120, 60}), "fallback origin replay changed recorded wheel units");
    require(initialRestoreWheelInputs(0).empty(), "zero wheel origin produced replay input");
}

void testControlledCaptureRequiresTwoNoMotionAttempts() {
    require(!controlledNoMotionEnded(0), "zero no-motion attempts ended capture");
    require(!controlledNoMotionEnded(1), "first no-motion attempt ended capture");
    require(controlledNoMotionEnded(2), "second no-motion attempt did not end capture");
}

void testControlledSelectionCropPreservesViewportInsets() {
    Options opt;
    opt.cropY = 100;
    opt.cropH = 300;
    require(controlledSelectionCropHeight(1600, 500, opt, false) == 1400, "incomplete capture lost selected viewport insets");
    require(controlledSelectionCropHeight(1600, 500, opt, true) == 1500, "page-end capture discarded the final viewport tail");
}

void testControlledPolicyAdoptsOnlyMovedSettledFrames() {
    ControlledLoopPolicyState state;
    require(!applyControlledObservation(state, ControlledObservation::NoMotion), "no-motion frame was adopted");
    require(state.acceptedFrames == 1 && state.consecutiveNoMotion == 1 && !state.finished, "first no-motion state differs");
    require(applyControlledObservation(state, ControlledObservation::MovedSettled), "moved settled frame was not adopted");
    require(state.acceptedFrames == 2 && state.consecutiveNoMotion == 0, "successful movement did not reset no-motion state");
    require(!applyControlledObservation(state, ControlledObservation::NoMotion), "later no-motion frame was adopted");
    require(!applyControlledObservation(state, ControlledObservation::NoMotion), "second no-motion frame was adopted");
    require(state.finished && state.acceptedFrames == 2, "two consecutive no-motion attempts did not finish without adoption");
    require(!applyControlledObservation(state, ControlledObservation::MovedSettled), "finished loop accepted another frame");
}

void testControlledPolicyStopsAtUiaBottomAndSettleTimeout() {
    ControlledLoopPolicyState bottom;
    require(!applyControlledObservation(bottom, ControlledObservation::UiaBottom), "UIA bottom adopted a frame");
    require(bottom.finished && bottom.acceptedFrames == 1, "UIA bottom did not stop before input");
    ControlledLoopPolicyState timeout;
    require(!applyControlledObservation(timeout, ControlledObservation::SettleTimeout), "settle timeout adopted a frame");
    require(timeout.finished && timeout.acceptedFrames == 1, "settle timeout did not stop without adoption");
}

void testEverySentWheelIsBookkeptUntilProvenStatic() {
    WheelScrollGuard guard;
    guard.record(-WHEEL_DELTA);
    require(guard.forwardDeltas == std::vector<int>({-WHEEL_DELTA}), "sent wheel was absent from restoration history");
    guard.forwardMovements.back() = 73;
    require(guard.pendingRestoreDeltas() == std::vector<int>({WHEEL_DELTA}), "ambiguous moved wheel would not be reversed");
    guard.record(-WHEEL_DELTA);
    guard.discardLast();
    require(guard.forwardDeltas.size() == 1, "proven static wheel was not discarded independently");
}

void testRestorePrecedesCompositionAndFailureSuppressesIt() {
    std::vector<std::string> trace;
    bool ok = restoreBeforeCompose(
        [&]() { trace.push_back("restore"); return true; },
        [&]() { trace.push_back("stitch"); });
    require(ok && trace == std::vector<std::string>({"restore", "stitch"}), "composition did not follow successful restoration");
    trace.clear();
    ok = restoreBeforeCompose(
        [&]() { trace.push_back("restore"); return false; },
        [&]() { trace.push_back("stitch"); });
    require(!ok && trace == std::vector<std::string>({"restore"}), "restoration failure did not suppress composition");
}

void testWheelTargetPointClampsToWindow() {
    RECT rect{100, 200, 500, 600};
    POINT center = wheelTargetPoint(rect, std::numeric_limits<int>::min(), std::numeric_limits<int>::min());
    require(center.x == 300 && center.y == 400, "default wheel point differs");
    POINT clamped = wheelTargetPoint(rect, -50, 900);
    require(clamped.x == 100 && clamped.y == 599, "wheel point escaped the target window");
}

void testControlledSettleAcceptsStableDocumentPosition() {
    require(controlledMovementPositionStable(240, 240), "unchanged document position was not stable");
    require(controlledMovementPositionStable(240, 242), "minor alignment jitter was not stable");
    require(!controlledMovementPositionStable(240, 260), "continuing scroll motion was considered stable");
    require(!controlledMovementPositionStable(-1, 240), "first movement observation was considered stable");
}

void testControlledSettleAcceptsQuietWgcAfterMotion() {
    require(controlledQuietPeriodSettles(true, true), "quiet WGC after measured motion was not settled");
    require(!controlledQuietPeriodSettles(false, true), "quiet WGC without motion was settled");
    require(!controlledQuietPeriodSettles(true, false), "quiet WGC without a captured candidate was settled");
}

void testControlledStepKeepsMeasuredFrameWhenDynamicUiNeverSettles() {
    require(controlledStepCanBeAccepted(true, false, true), "measured final frame was rejected only because dynamic UI did not settle");
    require(controlledStepCanBeAccepted(true, true, true), "settled measured frame was rejected");
    require(!controlledStepCanBeAccepted(false, false, true), "unmoved frame was accepted");
    require(!controlledStepCanBeAccepted(true, false, false), "missing frame was accepted");
}

void testControlledMovementRejectsGapsAndBuildsOverlapPriors() {
    require(controlledMovementUsable(120, 600), "normal controlled movement was rejected");
    require(!controlledMovementUsable(590, 600), "movement that leaves a document gap was accepted");
    std::vector<Bitmap> frames{makeFrame(96, 600, 0), makeFrame(96, 600, 120), makeFrame(96, 600, 240)};
    std::vector<int> estimates = controlledOverlapEstimates(frames, {0, 120, 120});
    require(estimates == std::vector<int>({-1, 480, 480}), "controlled overlap priors differ from measured motion");
    std::vector<int> origins;
    Bitmap stitched = stitchVertical(frames, 96, estimates, &origins, false);
    require(origins == std::vector<int>({0, 120, 240}), "controlled priors produced gaps between frames");
    requireSourcePixels(stitched);
}

void testUiaProgressSupportsSmallAmbiguousMovement() {
    int movement = controlledUiaMovementEstimate(600, 10, 12, 20, 96);
    require(movement > 0 && controlledMovementUsable(movement, 600), "UIA progress did not produce a usable movement estimate");
    require(controlledUiaMovementEstimate(600, 12, 12, 20, 96) < 0, "unchanged UIA progress produced movement");
}

void testControlledMeasurementUsesSelectedRegion() {
    Bitmap frame = makeFrame(96, 120, 0);
    Options opt;
    opt.cropX = 20;
    opt.cropY = 30;
    opt.cropW = 40;
    opt.cropH = 50;
    Bitmap measured = controlledMeasurementBitmap(frame, opt);
    require(measured.width == 40 && measured.height == 50, "controlled measurement ignored selected region");
    Bitmap expected = cropBitmap(frame, 20, 30, 40, 50);
    require(measured.bgra == expected.bgra, "controlled measurement pixels differ from selected region");
}

void testParserAllowsLongControlledCapture() {
    char arg0[] = "helper";
    char arg1[] = "capture-scroll";
    char arg2[] = "--hwnd";
    char arg3[] = "1";
    char arg4[] = "--out";
    char arg5[] = "capture.png";
    char arg6[] = "--max-frames";
    char arg7[] = "96";
    char* argv[] = {arg0, arg1, arg2, arg3, arg4, arg5, arg6, arg7};
    Options opt = parseArgs(8, argv);
    require(opt.maxFrames == 96, "long controlled capture was clamped below 96 frames");
}

void testRestoreFallsBackWhenUiaRestoreFails() {
    std::vector<std::string> trace;
    bool restored = restoreWithFallback(
        [&]() { trace.push_back("uia"); return false; },
        [&]() { trace.push_back("wheel"); return true; });
    require(restored, "wheel fallback did not recover failed UIA restoration");
    require(trace == std::vector<std::string>({"uia", "wheel"}), "restore fallback order differs");
    trace.clear();
    restored = restoreWithFallback(
        [&]() { trace.push_back("uia"); return true; },
        [&]() { trace.push_back("wheel"); return true; });
    require(restored && trace == std::vector<std::string>({"uia"}), "successful UIA restoration also ran wheel fallback");
}

} // namespace

#define RUN_TEST(testName) do { \
    try { testName(); } catch (std::exception const& err) { \
        throw std::runtime_error(std::string(#testName) + ": " + err.what()); \
    } \
} while (false)

int main() {
    try {
        RUN_TEST(testExactVariableOverlap);
        RUN_TEST(testStickyEdgesAppearOnce);
        RUN_TEST(testStickyHeaderTallerThanMovementAppearsOnce);
        RUN_TEST(testThinStableBorderExpandsDynamicPanel);
        RUN_TEST(testLShapedFixedChromeDoesNotMaskBoundingBox);
        RUN_TEST(testMiddleFloatingOverlayIsRemoved);
        RUN_TEST(testTwoFrameFloatingOverlayIsRemovedConservatively);
        RUN_TEST(testDynamicFixedInteriorIsRemovedWithStableBorder);
        RUN_TEST(testBorderlessDynamicFixedPanelIsRemoved);
        RUN_TEST(testTranslatedDynamicBodyIsNotFixed);
        RUN_TEST(testPersistentFixedSidebarUsesDeterministicFallback);
        RUN_TEST(testSparseUnrecoverableMaskUsesFallback);
        RUN_TEST(testPartialFixedHeaderAndFooterAppearOnce);
        RUN_TEST(testTemporaryFloatingPopupIsRemovedByConsensus);
        RUN_TEST(testLongTemporaryPopupKeepsCleanEdgeFramesInConsensus);
        RUN_TEST(testRepeatingPatternIsRejected);
        RUN_TEST(testRepeatingPatternUsesMeasuredPrior);
        RUN_TEST(testIncorrectPriorUsesVisualOverlap);
        RUN_TEST(testVariableRangeMovementUsesVisualOrigins);
        RUN_TEST(testRangeCropUsesMeasuredFrameOrigins);
        RUN_TEST(testSelectionWidthCompositionMatchesFullDocumentCrop);
        RUN_TEST(testCropRejectsOutOfBounds);
        RUN_TEST(testDimensionChangeIsRejected);
        RUN_TEST(testRangeCoverageRequiresBottomSafety);
        RUN_TEST(testRangeStepBatchPreservesOverlap);
        RUN_TEST(testStoredRangeFrameKeepsFullWidthEvidence);
        RUN_TEST(testPartialWheelBatchRestoresOnlyMovedEvents);
        RUN_TEST(testStitchedOutputMemoryBudgetIsEnforced);
        RUN_TEST(testRangeParserRejectsMoreThanSixtyFourSteps);
        RUN_TEST(testScrollPositionCommandsDoNotRequireOutputPath);
        RUN_TEST(testFallbackOriginReplayUsesRecordedWheelUnits);
        RUN_TEST(testControlledCaptureRequiresTwoNoMotionAttempts);
        RUN_TEST(testControlledSelectionCropPreservesViewportInsets);
        RUN_TEST(testControlledPolicyAdoptsOnlyMovedSettledFrames);
        RUN_TEST(testControlledPolicyStopsAtUiaBottomAndSettleTimeout);
        RUN_TEST(testEverySentWheelIsBookkeptUntilProvenStatic);
        RUN_TEST(testRestorePrecedesCompositionAndFailureSuppressesIt);
        RUN_TEST(testWheelTargetPointClampsToWindow);
        RUN_TEST(testControlledSettleAcceptsStableDocumentPosition);
        RUN_TEST(testControlledSettleAcceptsQuietWgcAfterMotion);
        RUN_TEST(testControlledStepKeepsMeasuredFrameWhenDynamicUiNeverSettles);
        RUN_TEST(testControlledMovementRejectsGapsAndBuildsOverlapPriors);
        RUN_TEST(testUiaProgressSupportsSmallAmbiguousMovement);
        RUN_TEST(testControlledMeasurementUsesSelectedRegion);
        RUN_TEST(testParserAllowsLongControlledCapture);
        RUN_TEST(testRestoreFallsBackWhenUiaRestoreFails);
        std::cout << "scroll stitch tests passed\n";
        return 0;
    } catch (std::exception const& err) {
        std::cerr << err.what() << '\n';
        return 1;
    }
}
