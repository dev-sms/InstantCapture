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

Bitmap stitchVertical(std::vector<Bitmap> const& frames, int overlapPx) {
    if (frames.empty()) {
        throw std::runtime_error("no frames captured for scrolling screenshot");
    }
    int width = frames.front().width;
    int totalHeight = frames.front().height;
    for (size_t i = 1; i < frames.size(); ++i) {
        if (frames[i].empty()) {
            continue;
        }
        width = (std::min)(width, frames[i].width);
        int overlap = (std::min)(overlapPx, (std::max)(0, frames[i].height - 1));
        totalHeight += (std::max)(1, frames[i].height - overlap);
    }
    Bitmap out;
    out.width = width;
    out.height = totalHeight;
    out.bgra.assign(static_cast<size_t>(out.width) * static_cast<size_t>(out.height) * 4, 0);

    int dstY = 0;
    for (size_t i = 0; i < frames.size(); ++i) {
        Bitmap const& frame = frames[i];
        if (frame.empty()) {
            continue;
        }
        int skipTop = i == 0 ? 0 : (std::min)(overlapPx, (std::max)(0, frame.height - 1));
        int copyHeight = frame.height - skipTop;
        int copyWidth = (std::min)(width, frame.width);
        for (int y = 0; y < copyHeight; ++y) {
            auto src = frame.bgra.data() + (static_cast<size_t>(y + skipTop) * frame.width * 4);
            auto dst = out.bgra.data() + (static_cast<size_t>(dstY + y) * out.width * 4);
            std::copy(src, src + static_cast<size_t>(copyWidth) * 4, dst);
        }
        dstY += copyHeight;
    }
    return out;
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

    auto pattern = findScrollPattern(opt.hwnd);
    if (!pattern) {
        result.errorCode = "UIA_SCROLL_UNSUPPORTED";
        result.message = "No vertically scrollable UI Automation ScrollPattern was found for this window.";
        return result;
    }

    winrt::com_ptr<ID3D11Device> d3dDevice = createD3DDevice();
    WGD3D::IDirect3DDevice winrtDevice = createWinRTDevice(d3dDevice.get());
    WGC::GraphicsCaptureItem item = createCaptureItem(opt.hwnd);

    double initialPercent = -1;
    pattern->get_CurrentVerticalScrollPercent(&initialPercent);
    ScrollPositionGuard scrollRestore{ pattern.get(), initialPercent };
    std::vector<Bitmap> frames;
    frames.reserve(static_cast<size_t>(opt.maxFrames));
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
        frames.push_back(captureWindowBitmap(d3dDevice.get(), winrtDevice, item, opt.timeoutMs));
    }

    Bitmap stitched = stitchVertical(frames, opt.overlapPx);
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
