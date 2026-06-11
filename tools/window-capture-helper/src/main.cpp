#ifndef WIN32_LEAN_AND_MEAN
#define WIN32_LEAN_AND_MEAN
#endif
#ifndef NOMINMAX
#define NOMINMAX
#endif
#ifndef _SILENCE_CLANG_COROUTINE_MESSAGE
#define _SILENCE_CLANG_COROUTINE_MESSAGE
#endif

#include <windows.h>
#include <d3d11.h>
#include <dxgi1_2.h>
#include <UIAutomationClient.h>
#include <wincodec.h>
#include <windows.graphics.capture.interop.h>
#include <windows.graphics.directx.direct3d11.interop.h>
#include <winrt/base.h>

namespace winrt::impl {
template <typename Async>
auto wait_for(Async const& async, Windows::Foundation::TimeSpan const& timeout);
}

#include <winrt/Windows.Foundation.h>
#include <winrt/Windows.Graphics.Capture.h>
#include <winrt/Windows.Graphics.DirectX.h>
#include <winrt/Windows.Graphics.DirectX.Direct3D11.h>

#include <algorithm>
#include <chrono>
#include <cmath>
#include <condition_variable>
#include <cstdint>
#include <iomanip>
#include <iostream>
#include <limits>
#include <mutex>
#include <sstream>
#include <stdexcept>
#include <string>
#include <thread>
#include <vector>

namespace WGC = winrt::Windows::Graphics::Capture;
namespace WGD = winrt::Windows::Graphics::DirectX;
namespace WGD3D = winrt::Windows::Graphics::DirectX::Direct3D11;
namespace WGraphics = winrt::Windows::Graphics;

namespace {

struct Options {
    std::string command;
    HWND hwnd{};
    std::wstring outputPath;
    bool json = false;
    int timeoutMs = 2000;
    int maxFrames = 8;
    int delayMs = 250;
    int overlapPx = 96;
    int cropX = 0;
    int cropY = 0;
    int cropW = 0;
    int cropH = 0;
    int rangeSteps = 0;
    int rangeEndY = 0;
    bool rangeRequested = false;
    int wheelX = std::numeric_limits<int>::min();
    int wheelY = std::numeric_limits<int>::min();
};

struct CaptureResult {
    bool ok = false;
    std::string method = "wgc";
    std::wstring outputPath;
    int width = 0;
    int height = 0;
    std::string errorCode;
    std::string message;
};

struct Bitmap {
    int width = 0;
    int height = 0;
    std::vector<uint8_t> bgra;

    bool empty() const {
        return width <= 0 || height <= 0 || bgra.empty();
    }
};

struct StitchSearchResult {
    int skipTop = 0;
    double score = std::numeric_limits<double>::infinity();
    bool measured = false;
};

int medianOfRecent(std::vector<int> const& values, size_t maxCount) {
    if (values.empty()) {
        return -1;
    }
    size_t count = (std::min)(values.size(), maxCount);
    std::vector<int> recent(values.end() - count, values.end());
    std::sort(recent.begin(), recent.end());
    return recent[recent.size() / 2];
}

int smoothMeasuredOverlap(int current, std::vector<int> const& measured) {
    if (measured.size() < 2) {
        return current;
    }
    size_t count = (std::min)(measured.size(), static_cast<size_t>(2));
    std::vector<int> recent(measured.end() - count, measured.end());
    recent.push_back(current);
    std::sort(recent.begin(), recent.end());
    int median = recent[recent.size() / 2];
    return std::abs(current - median) > 3 ? median : current;
}

std::wstring widen(std::string const& value) {
    if (value.empty()) {
        return {};
    }
    int size = MultiByteToWideChar(CP_UTF8, 0, value.data(), static_cast<int>(value.size()), nullptr, 0);
    if (size <= 0) {
        throw std::runtime_error("invalid UTF-8 argument");
    }
    std::wstring out(static_cast<size_t>(size), L'\0');
    MultiByteToWideChar(CP_UTF8, 0, value.data(), static_cast<int>(value.size()), &out[0], size);
    return out;
}

std::string narrow(std::wstring const& value) {
    if (value.empty()) {
        return {};
    }
    int size = WideCharToMultiByte(CP_UTF8, 0, value.data(), static_cast<int>(value.size()), nullptr, 0, nullptr, nullptr);
    if (size <= 0) {
        return {};
    }
    std::string out(static_cast<size_t>(size), '\0');
    WideCharToMultiByte(CP_UTF8, 0, value.data(), static_cast<int>(value.size()), &out[0], size, nullptr, nullptr);
    return out;
}

std::string jsonEscape(std::string const& value) {
    std::ostringstream out;
    for (char ch : value) {
        switch (ch) {
        case '\\': out << "\\\\"; break;
        case '"': out << "\\\""; break;
        case '\b': out << "\\b"; break;
        case '\f': out << "\\f"; break;
        case '\n': out << "\\n"; break;
        case '\r': out << "\\r"; break;
        case '\t': out << "\\t"; break;
        default:
            if (static_cast<unsigned char>(ch) < 0x20) {
                out << "\\u" << std::hex << std::setw(4) << std::setfill('0') << static_cast<int>(ch);
            } else {
                out << ch;
            }
        }
    }
    return out.str();
}

void printJson(CaptureResult const& result) {
    std::cout << "{\"ok\":" << (result.ok ? "true" : "false")
              << ",\"method\":\"" << jsonEscape(result.method) << "\""
              << ",\"outputPath\":\"" << jsonEscape(narrow(result.outputPath)) << "\""
              << ",\"width\":" << result.width
              << ",\"height\":" << result.height;
    if (!result.ok) {
        std::cout << ",\"errorCode\":\"" << jsonEscape(result.errorCode) << "\""
                  << ",\"message\":\"" << jsonEscape(result.message) << "\"";
    }
    std::cout << "}" << std::endl;
}

HWND parseHwnd(std::string const& value) {
    size_t consumed = 0;
    unsigned long long raw = std::stoull(value, &consumed, 0);
    if (consumed != value.size() || raw == 0) {
        throw std::runtime_error("--hwnd must be a non-zero integer or hex value");
    }
    return reinterpret_cast<HWND>(static_cast<uintptr_t>(raw));
}

Options parseArgs(int argc, char** argv) {
    if (argc < 2) {
        throw std::runtime_error("usage: instantcapture-window-helper <capture-window|capture-scroll> --hwnd 0x... --out path [--json]");
    }
    std::string command = argv[1];
    if (command != "capture-window" && command != "capture-scroll") {
        throw std::runtime_error("usage: instantcapture-window-helper <capture-window|capture-scroll> --hwnd 0x... --out path [--json]");
    }

    Options opt;
    opt.command = command;
    for (int i = 2; i < argc; ++i) {
        std::string arg = argv[i];
        auto requireValue = [&](char const* name) -> std::string {
            if (i + 1 >= argc) {
                throw std::runtime_error(std::string(name) + " requires a value");
            }
            return argv[++i];
        };

        if (arg == "--hwnd") {
            opt.hwnd = parseHwnd(requireValue("--hwnd"));
        } else if (arg == "--out") {
            opt.outputPath = widen(requireValue("--out"));
        } else if (arg == "--json") {
            opt.json = true;
        } else if (arg == "--timeout-ms") {
            opt.timeoutMs = (std::max)(100, std::stoi(requireValue("--timeout-ms")));
        } else if (arg == "--max-frames") {
            opt.maxFrames = (std::min)(32, (std::max)(1, std::stoi(requireValue("--max-frames"))));
        } else if (arg == "--delay-ms") {
            opt.delayMs = (std::min)(2000, (std::max)(0, std::stoi(requireValue("--delay-ms"))));
        } else if (arg == "--overlap-px") {
            opt.overlapPx = (std::min)(1000, (std::max)(0, std::stoi(requireValue("--overlap-px"))));
        } else if (arg == "--crop-x") {
            opt.cropX = (std::max)(0, std::stoi(requireValue("--crop-x")));
        } else if (arg == "--crop-y") {
            opt.cropY = (std::max)(0, std::stoi(requireValue("--crop-y")));
        } else if (arg == "--crop-w") {
            opt.cropW = (std::max)(0, std::stoi(requireValue("--crop-w")));
        } else if (arg == "--crop-h") {
            opt.cropH = (std::max)(0, std::stoi(requireValue("--crop-h")));
        } else if (arg == "--range-steps") {
            opt.rangeRequested = true;
            opt.rangeSteps = (std::min)(64, (std::max)(0, std::stoi(requireValue("--range-steps"))));
        } else if (arg == "--range-end-y") {
            opt.rangeRequested = true;
            opt.rangeEndY = (std::max)(0, std::stoi(requireValue("--range-end-y")));
        } else if (arg == "--wheel-x") {
            opt.wheelX = std::stoi(requireValue("--wheel-x"));
        } else if (arg == "--wheel-y") {
            opt.wheelY = std::stoi(requireValue("--wheel-y"));
        } else if (arg == "--method") {
            std::string method = requireValue("--method");
            if (method != "wgc" && method != "auto") {
                throw std::runtime_error("prototype supports only --method wgc or auto");
            }
        } else {
            throw std::runtime_error("unknown argument: " + arg);
        }
    }

    if (!opt.hwnd) {
        throw std::runtime_error("--hwnd is required");
    }
    if (opt.outputPath.empty()) {
        throw std::runtime_error("--out is required");
    }
    return opt;
}

winrt::com_ptr<ID3D11Device> createD3DDevice() {
    UINT flags = D3D11_CREATE_DEVICE_BGRA_SUPPORT;
#if defined(_DEBUG)
    flags |= D3D11_CREATE_DEVICE_DEBUG;
#endif

    D3D_FEATURE_LEVEL featureLevels[] = {
        D3D_FEATURE_LEVEL_11_1,
        D3D_FEATURE_LEVEL_11_0,
        D3D_FEATURE_LEVEL_10_1,
        D3D_FEATURE_LEVEL_10_0,
    };

    winrt::com_ptr<ID3D11Device> device;
    winrt::com_ptr<ID3D11DeviceContext> context;
    D3D_FEATURE_LEVEL selected{};
    HRESULT hr = D3D11CreateDevice(
        nullptr,
        D3D_DRIVER_TYPE_HARDWARE,
        nullptr,
        flags,
        featureLevels,
        ARRAYSIZE(featureLevels),
        D3D11_SDK_VERSION,
        device.put(),
        &selected,
        context.put());
    if (FAILED(hr)) {
        winrt::check_hresult(D3D11CreateDevice(
            nullptr,
            D3D_DRIVER_TYPE_WARP,
            nullptr,
            D3D11_CREATE_DEVICE_BGRA_SUPPORT,
            featureLevels,
            ARRAYSIZE(featureLevels),
            D3D11_SDK_VERSION,
            device.put(),
            &selected,
            context.put()));
    }
    return device;
}

WGD3D::IDirect3DDevice createWinRTDevice(ID3D11Device* device) {
    winrt::com_ptr<IDXGIDevice> dxgiDevice;
    winrt::check_hresult(device->QueryInterface(dxgiDevice.put()));

    winrt::com_ptr<::IInspectable> inspectable;
    winrt::check_hresult(CreateDirect3D11DeviceFromDXGIDevice(dxgiDevice.get(), inspectable.put()));
    return inspectable.as<WGD3D::IDirect3DDevice>();
}

WGC::GraphicsCaptureItem createCaptureItem(HWND hwnd) {
    if (!IsWindow(hwnd)) {
        throw std::runtime_error("HWND is not a valid window");
    }
    auto interop = winrt::get_activation_factory<WGC::GraphicsCaptureItem, IGraphicsCaptureItemInterop>();
    WGC::GraphicsCaptureItem item{ nullptr };
    winrt::check_hresult(interop->CreateForWindow(hwnd, winrt::guid_of<WGC::IGraphicsCaptureItem>(), winrt::put_abi(item)));
    return item;
}

winrt::com_ptr<ID3D11Texture2D> textureFromSurface(WGD3D::IDirect3DSurface const& surface) {
    auto access = surface.as<::Windows::Graphics::DirectX::Direct3D11::IDirect3DDxgiInterfaceAccess>();
    winrt::com_ptr<ID3D11Texture2D> texture;
    winrt::check_hresult(access->GetInterface(__uuidof(ID3D11Texture2D), texture.put_void()));
    return texture;
}

Bitmap bitmapFromTexture(ID3D11Device* device, ID3D11Texture2D* source, WGraphics::SizeInt32 size) {
    if (size.Width <= 0 || size.Height <= 0) {
        throw std::runtime_error("captured frame has empty content size");
    }

    winrt::com_ptr<ID3D11DeviceContext> context;
    device->GetImmediateContext(context.put());

    D3D11_TEXTURE2D_DESC desc{};
    source->GetDesc(&desc);
    desc.Width = static_cast<UINT>(size.Width);
    desc.Height = static_cast<UINT>(size.Height);
    desc.MipLevels = 1;
    desc.ArraySize = 1;
    desc.BindFlags = 0;
    desc.MiscFlags = 0;
    desc.Usage = D3D11_USAGE_STAGING;
    desc.CPUAccessFlags = D3D11_CPU_ACCESS_READ;

    winrt::com_ptr<ID3D11Texture2D> staging;
    winrt::check_hresult(device->CreateTexture2D(&desc, nullptr, staging.put()));

    D3D11_BOX box{};
    box.left = 0;
    box.top = 0;
    box.front = 0;
    box.right = static_cast<UINT>(size.Width);
    box.bottom = static_cast<UINT>(size.Height);
    box.back = 1;
    context->CopySubresourceRegion(staging.get(), 0, 0, 0, 0, source, 0, &box);

    D3D11_MAPPED_SUBRESOURCE mapped{};
    winrt::check_hresult(context->Map(staging.get(), 0, D3D11_MAP_READ, 0, &mapped));
    struct UnmapGuard {
        ID3D11DeviceContext* context{};
        ID3D11Texture2D* texture{};
        ~UnmapGuard() {
            if (context && texture) {
                context->Unmap(texture, 0);
            }
        }
    } unmap{ context.get(), staging.get() };

    Bitmap bitmap;
    bitmap.width = size.Width;
    bitmap.height = size.Height;
    bitmap.bgra.resize(static_cast<size_t>(bitmap.width) * static_cast<size_t>(bitmap.height) * 4);
    uint8_t* src = static_cast<uint8_t*>(mapped.pData);
    for (int y = 0; y < bitmap.height; ++y) {
        std::copy(src + static_cast<size_t>(y) * mapped.RowPitch,
                  src + static_cast<size_t>(y) * mapped.RowPitch + static_cast<size_t>(bitmap.width) * 4,
                  bitmap.bgra.data() + static_cast<size_t>(y) * static_cast<size_t>(bitmap.width) * 4);
    }
    return bitmap;
}

void saveBitmapAsPng(Bitmap const& bitmap, std::wstring const& path) {
    if (bitmap.empty()) {
        throw std::runtime_error("bitmap is empty");
    }

    winrt::com_ptr<IWICImagingFactory> factory;
    winrt::check_hresult(CoCreateInstance(CLSID_WICImagingFactory2, nullptr, CLSCTX_INPROC_SERVER, IID_PPV_ARGS(factory.put())));

    winrt::com_ptr<IWICStream> stream;
    winrt::check_hresult(factory->CreateStream(stream.put()));
    winrt::check_hresult(stream->InitializeFromFilename(path.c_str(), GENERIC_WRITE));

    winrt::com_ptr<IWICBitmapEncoder> encoder;
    winrt::check_hresult(factory->CreateEncoder(GUID_ContainerFormatPng, nullptr, encoder.put()));
    winrt::check_hresult(encoder->Initialize(stream.get(), WICBitmapEncoderNoCache));

    winrt::com_ptr<IWICBitmapFrameEncode> frame;
    winrt::com_ptr<IPropertyBag2> properties;
    winrt::check_hresult(encoder->CreateNewFrame(frame.put(), properties.put()));
    winrt::check_hresult(frame->Initialize(properties.get()));
    winrt::check_hresult(frame->SetSize(static_cast<UINT>(bitmap.width), static_cast<UINT>(bitmap.height)));

    WICPixelFormatGUID format = GUID_WICPixelFormat32bppBGRA;
    winrt::check_hresult(frame->SetPixelFormat(&format));
    if (!IsEqualGUID(format, GUID_WICPixelFormat32bppBGRA)) {
        throw std::runtime_error("WIC PNG encoder did not accept BGRA pixels");
    }

    winrt::check_hresult(frame->WritePixels(
        static_cast<UINT>(bitmap.height),
        static_cast<UINT>(bitmap.width * 4),
        static_cast<UINT>(bitmap.bgra.size()),
        const_cast<BYTE*>(reinterpret_cast<BYTE const*>(bitmap.bgra.data()))));
    winrt::check_hresult(frame->Commit());
    winrt::check_hresult(encoder->Commit());
}

void saveTextureAsPng(ID3D11Device* device, ID3D11Texture2D* source, WGraphics::SizeInt32 size, std::wstring const& path) {
    saveBitmapAsPng(bitmapFromTexture(device, source, size), path);
}

Bitmap captureWindowBitmap(ID3D11Device* d3dDevice, WGD3D::IDirect3DDevice const& winrtDevice, WGC::GraphicsCaptureItem const& item, int timeoutMs) {
    WGraphics::SizeInt32 size = item.Size();
    if (size.Width <= 0 || size.Height <= 0) {
        throw std::runtime_error("GraphicsCaptureItem has an empty size");
    }

    WGC::Direct3D11CaptureFrame captured{ nullptr };
    std::mutex mutex;
    std::condition_variable cv;
    bool arrived = false;

    auto framePool = WGC::Direct3D11CaptureFramePool::CreateFreeThreaded(
        winrtDevice,
        WGD::DirectXPixelFormat::B8G8R8A8UIntNormalized,
        1,
        size);
    auto session = framePool.CreateCaptureSession(item);
    auto token = framePool.FrameArrived([&](WGC::Direct3D11CaptureFramePool const& sender, winrt::Windows::Foundation::IInspectable const&) {
        std::lock_guard<std::mutex> lock(mutex);
        if (!captured) {
            captured = sender.TryGetNextFrame();
            arrived = static_cast<bool>(captured);
        }
        cv.notify_one();
    });

    session.StartCapture();
    {
        std::unique_lock<std::mutex> lock(mutex);
        cv.wait_for(lock, std::chrono::milliseconds(timeoutMs), [&] { return arrived; });
    }
    framePool.FrameArrived(token);
    session.Close();
    framePool.Close();

    if (!captured) {
        throw std::runtime_error("Timed out waiting for a WGC frame");
    }

    winrt::com_ptr<ID3D11Texture2D> texture = textureFromSurface(captured.Surface());
    return bitmapFromTexture(d3dDevice, texture.get(), captured.ContentSize());
}

CaptureResult captureWindow(Options const& opt) {
    CaptureResult result;
    result.outputPath = opt.outputPath;

    if (!WGC::GraphicsCaptureSession::IsSupported()) {
        result.errorCode = "WGC_UNSUPPORTED";
        result.message = "Windows Graphics Capture is not supported on this device.";
        return result;
    }

    winrt::com_ptr<ID3D11Device> d3dDevice = createD3DDevice();
    WGD3D::IDirect3DDevice winrtDevice = createWinRTDevice(d3dDevice.get());
    WGC::GraphicsCaptureItem item = createCaptureItem(opt.hwnd);

    Bitmap bitmap = captureWindowBitmap(d3dDevice.get(), winrtDevice, item, opt.timeoutMs);
    saveBitmapAsPng(bitmap, opt.outputPath);

    result.ok = true;
    result.width = bitmap.width;
    result.height = bitmap.height;
    return result;
}

winrt::com_ptr<IUIAutomationScrollPattern> scrollPatternFromElement(IUIAutomationElement* element) {
    if (!element) {
        return nullptr;
    }
    winrt::com_ptr<IUnknown> unknown;
    HRESULT hr = element->GetCurrentPattern(UIA_ScrollPatternId, unknown.put());
    if (FAILED(hr) || !unknown) {
        return nullptr;
    }
    winrt::com_ptr<IUIAutomationScrollPattern> pattern;
    hr = unknown->QueryInterface(IID_PPV_ARGS(pattern.put()));
    if (FAILED(hr) || !pattern) {
        return nullptr;
    }
    BOOL verticallyScrollable = FALSE;
    if (FAILED(pattern->get_CurrentVerticallyScrollable(&verticallyScrollable)) || !verticallyScrollable) {
        return nullptr;
    }
    return pattern;
}

winrt::com_ptr<IUIAutomationScrollPattern> findScrollPattern(HWND hwnd) {
    winrt::com_ptr<IUIAutomation> automation;
    winrt::check_hresult(CoCreateInstance(CLSID_CUIAutomation, nullptr, CLSCTX_INPROC_SERVER, IID_PPV_ARGS(automation.put())));

    winrt::com_ptr<IUIAutomationElement> root;
    winrt::check_hresult(automation->ElementFromHandle(hwnd, root.put()));
    if (auto pattern = scrollPatternFromElement(root.get())) {
        return pattern;
    }

    VARIANT scrollAvailable;
    VariantInit(&scrollAvailable);
    scrollAvailable.vt = VT_BOOL;
    scrollAvailable.boolVal = VARIANT_TRUE;
    winrt::com_ptr<IUIAutomationCondition> scrollCondition;
    if (SUCCEEDED(automation->CreatePropertyCondition(UIA_IsScrollPatternAvailablePropertyId, scrollAvailable, scrollCondition.put())) && scrollCondition) {
        winrt::com_ptr<IUIAutomationElementArray> matches;
        if (SUCCEEDED(root->FindAll(TreeScope_Subtree, scrollCondition.get(), matches.put())) && matches) {
            int length = 0;
            if (SUCCEEDED(matches->get_Length(&length))) {
                for (int i = 0; i < length; ++i) {
                    winrt::com_ptr<IUIAutomationElement> candidate;
                    if (FAILED(matches->GetElement(i, candidate.put())) || !candidate) {
                        continue;
                    }
                    if (auto pattern = scrollPatternFromElement(candidate.get())) {
                        return pattern;
                    }
                }
            }
        }
    }

    RECT rect{};
    if (!GetWindowRect(hwnd, &rect)) {
        return nullptr;
    }
    POINT center{ (rect.left + rect.right) / 2, (rect.top + rect.bottom) / 2 };
    winrt::com_ptr<IUIAutomationElement> element;
    if (SUCCEEDED(automation->ElementFromPoint(center, element.put())) && element) {
        winrt::com_ptr<IUIAutomationTreeWalker> walker;
        automation->get_ControlViewWalker(walker.put());
        for (int depth = 0; depth < 24 && element; ++depth) {
            if (auto pattern = scrollPatternFromElement(element.get())) {
                return pattern;
            }
            if (!walker) {
                break;
            }
            winrt::com_ptr<IUIAutomationElement> parent;
            if (FAILED(walker->GetParentElement(element.get(), parent.put())) || !parent) {
                break;
            }
            element = parent;
        }
    }

    winrt::com_ptr<IUIAutomationCondition> trueCondition;
    if (SUCCEEDED(automation->CreateTrueCondition(trueCondition.put())) && trueCondition) {
        winrt::com_ptr<IUIAutomationElementArray> descendants;
        if (SUCCEEDED(root->FindAll(TreeScope_Subtree, trueCondition.get(), descendants.put())) && descendants) {
            int length = 0;
            if (SUCCEEDED(descendants->get_Length(&length))) {
                int limit = (std::min)(length, 800);
                for (int i = 0; i < limit; ++i) {
                    winrt::com_ptr<IUIAutomationElement> candidate;
                    if (FAILED(descendants->GetElement(i, candidate.put())) || !candidate) {
                        continue;
                    }
                    if (auto pattern = scrollPatternFromElement(candidate.get())) {
                        return pattern;
                    }
                }
            }
        }
    }

    return nullptr;
}

struct ScrollPositionGuard {
    IUIAutomationScrollPattern* pattern{};
    double percent = -1;
    bool restored = false;

    ~ScrollPositionGuard() {
        restore();
    }

    void restore() {
        if (!restored && pattern && percent >= 0) {
            pattern->SetScrollPercent(-1.0, percent);
            restored = true;
        }
    }
};

int estimateSkipTopFromScroll(int prevHeight, int nextHeight, double beforePercent, double afterPercent, double viewSize, int fallback) {
    fallback = (std::min)(fallback, (std::max)(0, nextHeight - 1));
    if (prevHeight <= 0 || nextHeight <= 0 || beforePercent < 0 || afterPercent < 0 || afterPercent <= beforePercent || viewSize <= 0 || viewSize >= 100) {
        return fallback;
    }
    double scrollableFactor = (100.0 - viewSize) / viewSize;
    double shiftedPixels = static_cast<double>(prevHeight) * ((afterPercent - beforePercent) / 100.0) * scrollableFactor;
    int overlap = static_cast<int>(std::round(static_cast<double>(prevHeight) - shiftedPixels));
    return (std::min)((std::max)(0, overlap), (std::max)(0, nextHeight - 1));
}

Bitmap stitchVertical(std::vector<Bitmap> const& frames, int overlapPx, std::vector<int> const& estimatedSkipTops = {}, std::vector<int>* frameOffsets = nullptr) {
    if (frames.empty()) {
        throw std::runtime_error("no frames captured for scrolling screenshot");
    }

    auto pixelLuma = [](Bitmap const& bitmap, int x, int y) -> int {
        auto const* pixel = bitmap.bgra.data() + (static_cast<size_t>(y) * bitmap.width + x) * 4;
        return (29 * static_cast<int>(pixel[0]) + 150 * static_cast<int>(pixel[1]) + 77 * static_cast<int>(pixel[2])) >> 8;
    };

    auto pixelGradient = [&](Bitmap const& bitmap, int x, int y) -> int {
        if (x <= 0 || y <= 0 || x + 1 >= bitmap.width || y + 1 >= bitmap.height) {
            return 0;
        }
        int gx = std::abs(pixelLuma(bitmap, x + 1, y) - pixelLuma(bitmap, x - 1, y));
        int gy = std::abs(pixelLuma(bitmap, x, y + 1) - pixelLuma(bitmap, x, y - 1));
        return gx + gy;
    };

    auto bandScore = [&](Bitmap const& prev, Bitmap const& next, int prevStartY, int nextStartY, int compareHeight, int width) -> double {
        int scrollbarMargin = (std::min)(24, (std::max)(0, width * 12 / 1000));
        int xMin = 0;
        int xMax = width - scrollbarMargin;
        if (xMax <= xMin) {
            xMin = 0;
            xMax = width;
        }
        int rowStep = (std::max)(1, compareHeight / 32);
        int colStep = (std::max)(1, (xMax - xMin) / 96);
        double lumaA = 0;
        double lumaB = 0;
        double lumaAA = 0;
        double lumaBB = 0;
        double lumaAB = 0;
        double gradA = 0;
        double gradB = 0;
        double gradAA = 0;
        double gradBB = 0;
        double gradAB = 0;
        double totalDiff = 0;
        double count = 0;
        for (int y = 0; y < compareHeight; y += rowStep) {
            int py = prevStartY + y;
            int ny = nextStartY + y;
            for (int x = xMin; x < xMax; x += colStep) {
                double a = static_cast<double>(pixelLuma(prev, x, py));
                double b = static_cast<double>(pixelLuma(next, x, ny));
                lumaA += a;
                lumaB += b;
                lumaAA += a * a;
                lumaBB += b * b;
                lumaAB += a * b;
                totalDiff += std::abs(a - b);

                double ga = static_cast<double>(pixelGradient(prev, x, py));
                double gb = static_cast<double>(pixelGradient(next, x, ny));
                gradA += ga;
                gradB += gb;
                gradAA += ga * ga;
                gradBB += gb * gb;
                gradAB += ga * gb;
                count += 1.0;
            }
        }
        if (count < 16) {
            return std::numeric_limits<double>::infinity();
        }
        double meanDiff = totalDiff / count;
        auto nccDistance = [&](double sumA, double sumB, double sumAA, double sumBB, double sumAB) -> double {
            double numerator = sumAB - (sumA * sumB / count);
            double denomA = sumAA - (sumA * sumA / count);
            double denomB = sumBB - (sumB * sumB / count);
            double denominator = std::sqrt((std::max)(0.0, denomA) * (std::max)(0.0, denomB));
            if (denominator <= 1e-6) {
                return std::numeric_limits<double>::infinity();
            }
            double corr = numerator / denominator;
            corr = (std::min)(1.0, (std::max)(-1.0, corr));
            return (1.0 - corr) * 20.0;
        };
        double lumaScore = nccDistance(lumaA, lumaB, lumaAA, lumaBB, lumaAB);
        double gradScore = nccDistance(gradA, gradB, gradAA, gradBB, gradAB);
        double nccScore = (std::min)(lumaScore, gradScore);
        if (!std::isfinite(nccScore)) {
            if (meanDiff < 2.0) {
                return std::numeric_limits<double>::infinity();
            }
            return meanDiff;
        }
        if (meanDiff > 30.0) {
            return meanDiff;
        }
        return nccScore + meanDiff * 0.1;
    };

    auto searchBestSkip = [&](Bitmap const& prev, Bitmap const& next, int width, int compareHeight, int searchMin, int searchMax) -> StitchSearchResult {
        StitchSearchResult result;
        int prevStartY = prev.height - compareHeight;
        for (int skipTop = searchMin; skipTop <= searchMax; ++skipTop) {
            int nextStartY = skipTop - compareHeight;
            double score = bandScore(prev, next, prevStartY, nextStartY, compareHeight, width);
            if (score < result.score) {
                result.score = score;
                result.skipTop = skipTop;
            }
        }
        return result;
    };

    auto detectedSkipTop = [&](Bitmap const& prev, Bitmap const& next, int width, int estimate) -> StitchSearchResult {
        int fixedFallback = (std::min)(overlapPx, (std::max)(0, next.height - 1));
        int fallback = estimate >= 0 ? (std::min)((std::max)(0, estimate), (std::max)(0, next.height - 1)) : fixedFallback;
        int compareHeight = (std::min)({ 96, prev.height / 4, next.height / 4 });
        if (compareHeight < 12 || width <= 0) {
            return StitchSearchResult{ fallback, std::numeric_limits<double>::infinity(), false };
        }
        int fullMin = compareHeight;
        int fullMax = next.height - 1;
        if (fullMax < fullMin) {
            return StitchSearchResult{ fallback, std::numeric_limits<double>::infinity(), false };
        }

        constexpr double acceptScore = 10.0;
        if (estimate >= 0) {
            int radius = (std::max)(96, next.height / 8);
            int localMin = (std::max)(fullMin, fallback - radius);
            int localMax = (std::min)(fullMax, fallback + radius);
            StitchSearchResult local = searchBestSkip(prev, next, width, compareHeight, localMin, localMax);
            if (local.score <= acceptScore) {
                local.measured = true;
                return local;
            }
        }

        StitchSearchResult full = searchBestSkip(prev, next, width, compareHeight, fullMin, fullMax);
        if (full.score <= acceptScore) {
            full.measured = true;
            return full;
        }
        return StitchSearchResult{ fallback, full.score, false };
    };

    int width = frames.front().width;
    std::vector<int> skipTops(frames.size(), 0);
    std::vector<int> measuredOverlaps;
    int totalHeight = frames.front().height;
    for (size_t i = 1; i < frames.size(); ++i) {
        if (frames[i].empty()) {
            continue;
        }
        width = (std::min)(width, frames[i].width);
        int historyEstimate = medianOfRecent(measuredOverlaps, 10);
        int estimate = i < estimatedSkipTops.size() ? estimatedSkipTops[i] : historyEstimate;
        StitchSearchResult detected = detectedSkipTop(frames[i - 1], frames[i], width, estimate);
        if (detected.measured) {
            measuredOverlaps.push_back(detected.skipTop);
        } else if (historyEstimate >= 0) {
            detected.skipTop = (std::min)((std::max)(historyEstimate, 1), frames[i].height - 1);
        }
        skipTops[i] = detected.skipTop;
        totalHeight += (std::max)(1, frames[i].height - skipTops[i]);
    }
    Bitmap out;
    out.width = width;
    out.height = totalHeight;
    out.bgra.assign(static_cast<size_t>(out.width) * static_cast<size_t>(out.height) * 4, 0);
    if (frameOffsets) {
        frameOffsets->assign(frames.size(), 0);
    }

    int dstY = 0;
    for (size_t i = 0; i < frames.size(); ++i) {
        Bitmap const& frame = frames[i];
        if (frame.empty()) {
            continue;
        }
        int skipTop = i == 0 ? 0 : skipTops[i];
        int copyHeight = frame.height - skipTop;
        int copyWidth = (std::min)(width, frame.width);
        if (frameOffsets) {
            (*frameOffsets)[i] = dstY - skipTop;
        }
        for (int y = 0; y < copyHeight; ++y) {
            auto src = frame.bgra.data() + (static_cast<size_t>(y + skipTop) * frame.width * 4);
            auto dst = out.bgra.data() + (static_cast<size_t>(dstY + y) * out.width * 4);
            std::copy(src, src + static_cast<size_t>(copyWidth) * 4, dst);
        }
        dstY += copyHeight;
    }
    return out;
}

Bitmap cropBitmap(Bitmap const& src, int x, int y, int width, int height) {
    if (src.empty() || width <= 0 || height <= 0) {
        return src;
    }
    x = (std::min)((std::max)(0, x), src.width);
    y = (std::min)((std::max)(0, y), src.height);
    int right = (std::min)(src.width, x + width);
    int bottom = (std::min)(src.height, y + height);
    if (right <= x || bottom <= y) {
        return src;
    }

    Bitmap out;
    out.width = right - x;
    out.height = bottom - y;
    out.bgra.resize(static_cast<size_t>(out.width) * static_cast<size_t>(out.height) * 4);
    for (int row = 0; row < out.height; ++row) {
        auto const* srcRow = src.bgra.data() + (static_cast<size_t>(y + row) * src.width + x) * 4;
        auto* dstRow = out.bgra.data() + static_cast<size_t>(row) * out.width * 4;
        std::copy(srcRow, srcRow + static_cast<size_t>(out.width) * 4, dstRow);
    }
    return out;
}

double bitmapDifferenceScore(Bitmap const& a, Bitmap const& b) {
    if (a.empty() || b.empty()) {
        return std::numeric_limits<double>::infinity();
    }
    int width = (std::min)(a.width, b.width);
    int height = (std::min)(a.height, b.height);
    int rowStep = (std::max)(1, height / 48);
    int colStep = (std::max)(1, width / 96);
    uint64_t total = 0;
    uint64_t count = 0;
    for (int y = 0; y < height; y += rowStep) {
        for (int x = 0; x < width; x += colStep) {
            auto const* pa = a.bgra.data() + (static_cast<size_t>(y) * a.width + x) * 4;
            auto const* pb = b.bgra.data() + (static_cast<size_t>(y) * b.width + x) * 4;
            total += static_cast<uint64_t>(std::abs(static_cast<int>(pa[0]) - static_cast<int>(pb[0])));
            total += static_cast<uint64_t>(std::abs(static_cast<int>(pa[1]) - static_cast<int>(pb[1])));
            total += static_cast<uint64_t>(std::abs(static_cast<int>(pa[2]) - static_cast<int>(pb[2])));
            count += 3;
        }
    }
    if (count == 0) {
        return std::numeric_limits<double>::infinity();
    }
    return static_cast<double>(total) / static_cast<double>(count);
}

bool sendWheelInput(HWND hwnd, int delta, int wheelX = std::numeric_limits<int>::min(), int wheelY = std::numeric_limits<int>::min()) {
    RECT rect{};
    if (!GetWindowRect(hwnd, &rect)) {
        return false;
    }
    POINT original{};
    GetCursorPos(&original);
    POINT center{ (rect.left + rect.right) / 2, (rect.top + rect.bottom) / 2 };
    if (wheelX != std::numeric_limits<int>::min() && wheelY != std::numeric_limits<int>::min()) {
        center.x = static_cast<LONG>((std::min)((std::max)(wheelX, static_cast<int>(rect.left)), static_cast<int>(rect.right - 1)));
        center.y = static_cast<LONG>((std::min)((std::max)(wheelY, static_cast<int>(rect.top)), static_cast<int>(rect.bottom - 1)));
    }
    SetForegroundWindow(hwnd);
    SetCursorPos(center.x, center.y);
    INPUT input{};
    input.type = INPUT_MOUSE;
    input.mi.dwFlags = MOUSEEVENTF_WHEEL;
    input.mi.mouseData = static_cast<DWORD>(delta);
    UINT sent = SendInput(1, &input, sizeof(INPUT));
    std::this_thread::sleep_for(std::chrono::milliseconds(90));
    SetCursorPos(original.x, original.y);
    return sent == 1;
}

struct WheelScrollGuard {
    HWND hwnd{};
    int steps = 0;
    int delta = 0;
    bool restored = false;

    ~WheelScrollGuard() {
        restore();
    }

    void restore() {
        if (restored || !hwnd || steps <= 0 || delta == 0) {
            restored = true;
            return;
        }
        for (int i = 0; i < steps; ++i) {
            sendWheelInput(hwnd, delta);
        }
        std::this_thread::sleep_for(std::chrono::milliseconds(120));
        restored = true;
    }
};

CaptureResult captureScrollWithWheelFallback(Options const& opt, ID3D11Device* d3dDevice, WGD3D::IDirect3DDevice const& winrtDevice, WGC::GraphicsCaptureItem const& item) {
    CaptureResult result;
    result.method = "wgc-wheel-scroll";
    result.outputPath = opt.outputPath;

    std::vector<Bitmap> frames;
    frames.reserve(static_cast<size_t>(opt.maxFrames));
    frames.push_back(captureWindowBitmap(d3dDevice, winrtDevice, item, opt.timeoutMs));

    int wheelStep = opt.rangeSteps > 0 ? WHEEL_DELTA : WHEEL_DELTA * 5;
    int targetSteps = opt.rangeSteps > 0 ? opt.rangeSteps : opt.maxFrames - 1;
    WheelScrollGuard restore{ opt.hwnd, 0, wheelStep };
    for (int i = 1; i <= targetSteps && i < opt.maxFrames; ++i) {
        if (!sendWheelInput(opt.hwnd, -wheelStep, opt.wheelX, opt.wheelY)) {
            break;
        }
        ++restore.steps;
        if (opt.delayMs > 0) {
            std::this_thread::sleep_for(std::chrono::milliseconds(opt.delayMs));
        }
        Bitmap next = captureWindowBitmap(d3dDevice, winrtDevice, item, opt.timeoutMs);
        double score = bitmapDifferenceScore(frames.back(), next);
        if (score < 0.5) {
            if (!opt.rangeRequested) {
                break;
            }
            bool changed = false;
            for (int retry = 0; retry < 3; ++retry) {
                std::this_thread::sleep_for(std::chrono::milliseconds(150));
                next = captureWindowBitmap(d3dDevice, winrtDevice, item, opt.timeoutMs);
                score = bitmapDifferenceScore(frames.back(), next);
                if (score >= 0.5) {
                    changed = true;
                    break;
                }
            }
            if (!changed) {
                result.errorCode = "RANGE_SCROLL_INCOMPLETE";
                result.message = "Requested scroll range did not move after wheel input.";
                return result;
            }
        }
        frames.push_back(std::move(next));
    }

    std::vector<int> frameOffsets;
    Bitmap stitched = stitchVertical(frames, opt.overlapPx, {}, &frameOffsets);
    restore.restore();
    if (opt.cropW > 0 && opt.cropH > 0) {
        int cropY = opt.cropY;
        int cropH = opt.cropH;
        if (opt.rangeRequested && opt.rangeSteps > 0 && opt.rangeEndY > 0) {
            if (frameOffsets.size() <= static_cast<size_t>(opt.rangeSteps)) {
                result.errorCode = "RANGE_SCROLL_INCOMPLETE";
                result.message = "Requested scroll range was not fully captured.";
                return result;
            }
            size_t frameIndex = (std::min)(static_cast<size_t>(opt.rangeSteps), frameOffsets.size() - 1);
            int bottom = frameOffsets[frameIndex] + opt.rangeEndY;
            if (bottom <= cropY) {
                result.errorCode = "RANGE_SCROLL_INCOMPLETE";
                result.message = "Requested scroll range measured an invalid crop.";
                return result;
            }
            cropH = bottom - cropY;
        }
        stitched = cropBitmap(stitched, opt.cropX, cropY, opt.cropW, cropH);
    }
    saveBitmapAsPng(stitched, opt.outputPath);
    result.ok = true;
    result.width = stitched.width;
    result.height = stitched.height;
    return result;
}

CaptureResult captureScroll(Options const& opt) {
    CaptureResult result;
    result.method = "wgc-uia-scroll";
    result.outputPath = opt.outputPath;

    if (!WGC::GraphicsCaptureSession::IsSupported()) {
        result.errorCode = "WGC_UNSUPPORTED";
        result.message = "Windows Graphics Capture is not supported on this device.";
        return result;
    }

    winrt::com_ptr<ID3D11Device> d3dDevice = createD3DDevice();
    WGD3D::IDirect3DDevice winrtDevice = createWinRTDevice(d3dDevice.get());
    WGC::GraphicsCaptureItem item = createCaptureItem(opt.hwnd);

    if (opt.rangeRequested) {
        return captureScrollWithWheelFallback(opt, d3dDevice.get(), winrtDevice, item);
    }

    auto pattern = findScrollPattern(opt.hwnd);
    if (!pattern) {
        return captureScrollWithWheelFallback(opt, d3dDevice.get(), winrtDevice, item);
    }

    double initialPercent = -1;
    pattern->get_CurrentVerticalScrollPercent(&initialPercent);
    ScrollPositionGuard scrollRestore{ pattern.get(), initialPercent };
    std::vector<Bitmap> frames;
    frames.reserve(static_cast<size_t>(opt.maxFrames));
    std::vector<int> estimatedSkipTops;
    estimatedSkipTops.reserve(static_cast<size_t>(opt.maxFrames));
    estimatedSkipTops.push_back(0);
    frames.push_back(captureWindowBitmap(d3dDevice.get(), winrtDevice, item, opt.timeoutMs));

    double previousPercent = -1;
    for (int i = 1; i < opt.maxFrames; ++i) {
        double beforePercent = -1;
        if (FAILED(pattern->get_CurrentVerticalScrollPercent(&beforePercent))) {
            break;
        }
        if (beforePercent >= 99.9 || (previousPercent >= 0 && std::abs(beforePercent - previousPercent) < 0.01)) {
            break;
        }
        previousPercent = beforePercent;
        double viewSize = 20;
        pattern->get_CurrentVerticalViewSize(&viewSize);
        double step = (std::max)(5.0, (std::min)(45.0, viewSize * 0.85));
        double targetPercent = (std::min)(100.0, beforePercent + step);
        HRESULT scrollHr = pattern->SetScrollPercent(-1.0, targetPercent);
        if (FAILED(scrollHr)) {
            scrollHr = pattern->Scroll(ScrollAmount_NoAmount, ScrollAmount_LargeIncrement);
        }
        if (FAILED(scrollHr)) {
            break;
        }
        if (opt.delayMs > 0) {
            std::this_thread::sleep_for(std::chrono::milliseconds(opt.delayMs));
        }
        double afterPercent = -1;
        if (SUCCEEDED(pattern->get_CurrentVerticalScrollPercent(&afterPercent)) && std::abs(afterPercent - beforePercent) < 0.01) {
            break;
        }
        Bitmap nextFrame = captureWindowBitmap(d3dDevice.get(), winrtDevice, item, opt.timeoutMs);
        int estimate = estimateSkipTopFromScroll(frames.back().height, nextFrame.height, beforePercent, afterPercent, viewSize, opt.overlapPx);
        estimatedSkipTops.push_back(estimate);
        frames.push_back(std::move(nextFrame));
    }

    Bitmap stitched = stitchVertical(frames, opt.overlapPx, estimatedSkipTops);
    if (opt.cropW > 0 && opt.cropH > 0) {
        stitched = cropBitmap(stitched, opt.cropX, opt.cropY, opt.cropW, opt.cropH);
    }
    scrollRestore.restore();
    saveBitmapAsPng(stitched, opt.outputPath);
    result.ok = true;
    result.width = stitched.width;
    result.height = stitched.height;
    return result;
}

std::string hresultMessage(winrt::hresult_error const& err) {
    std::ostringstream out;
    out << narrow(std::wstring(err.message().c_str())) << " (HRESULT 0x" << std::hex << std::uppercase << static_cast<uint32_t>(err.code()) << ")";
    return out.str();
}

} // namespace

int main(int argc, char** argv) {
    CaptureResult result;
    try {
        winrt::init_apartment(winrt::apartment_type::multi_threaded);
        Options opt = parseArgs(argc, argv);
        result.outputPath = opt.outputPath;
        if (opt.command == "capture-scroll") {
            result = captureScroll(opt);
        } else {
            result = captureWindow(opt);
        }
        if (opt.json) {
            printJson(result);
        } else if (!result.ok) {
            std::cerr << result.errorCode << ": " << result.message << std::endl;
        }
        return result.ok ? 0 : 2;
    } catch (winrt::hresult_error const& err) {
        result.errorCode = "HRESULT_FAILURE";
        result.message = hresultMessage(err);
    } catch (std::exception const& err) {
        result.errorCode = "HELPER_FAILURE";
        result.message = err.what();
    }

    bool wantsJson = false;
    for (int i = 1; i < argc; ++i) {
        if (std::string(argv[i]) == "--json") {
            wantsJson = true;
            break;
        }
    }
    if (wantsJson) {
        printJson(result);
    } else {
        std::cerr << result.errorCode << ": " << result.message << std::endl;
    }
    return 2;
}
