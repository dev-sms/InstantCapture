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
#include <array>
#include <chrono>
#include <cmath>
#include <condition_variable>
#include <cstdint>
#include <functional>
#include <iomanip>
#include <iostream>
#include <limits>
#include <mutex>
#include <optional>
#include <sstream>
#include <stdexcept>
#include <string>
#include <thread>
#include <utility>
#include <vector>

namespace WGC = winrt::Windows::Graphics::Capture;
namespace WGD = winrt::Windows::Graphics::DirectX;
namespace WGD3D = winrt::Windows::Graphics::DirectX::Direct3D11;
namespace WGraphics = winrt::Windows::Graphics;

namespace {

constexpr int maxScrollFrames = 96;

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
    int rangeDistancePx = 0;
    int rangeEndY = 0;
    bool rangeRequested = false;
    int wheelX = std::numeric_limits<int>::min();
    int wheelY = std::numeric_limits<int>::min();
    double scrollPercent = -1;
    double initialScrollPercent = -1;
    int initialWheelUnits = 0;
};

struct CaptureResult {
    bool ok = false;
    std::string method = "wgc";
    std::wstring outputPath;
    int width = 0;
    int height = 0;
    std::string errorCode;
    std::string message;
    double scrollPercent = -1;
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
    double alternativeScore = std::numeric_limits<double>::infinity();
    bool measured = false;
    int stickyTop = 0;
    int stickyBottom = 0;
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
    if (result.scrollPercent >= 0) {
        std::cout << ",\"scrollPercent\":" << std::fixed << std::setprecision(6) << result.scrollPercent;
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
        throw std::runtime_error("usage: instantcapture-window-helper <capture-window|capture-scroll|query-scroll|restore-scroll> --hwnd 0x... [--out path] [--json]");
    }
    std::string command = argv[1];
    if (command != "capture-window" && command != "capture-scroll" && command != "query-scroll" && command != "restore-scroll") {
        throw std::runtime_error("unknown helper command");
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
            opt.maxFrames = (std::min)(maxScrollFrames, (std::max)(1, std::stoi(requireValue("--max-frames"))));
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
            opt.rangeSteps = std::stoi(requireValue("--range-steps"));
            if (opt.rangeSteps < 0 || opt.rangeSteps > 64) {
                throw std::runtime_error("--range-steps must be between 0 and 64");
            }
        } else if (arg == "--range-distance-px") {
            opt.rangeRequested = true;
            opt.rangeDistancePx = (std::max)(0, std::stoi(requireValue("--range-distance-px")));
        } else if (arg == "--range-end-y") {
            opt.rangeRequested = true;
            opt.rangeEndY = (std::max)(0, std::stoi(requireValue("--range-end-y")));
        } else if (arg == "--wheel-x") {
            opt.wheelX = std::stoi(requireValue("--wheel-x"));
        } else if (arg == "--wheel-y") {
            opt.wheelY = std::stoi(requireValue("--wheel-y"));
        } else if (arg == "--scroll-percent") {
            opt.scrollPercent = std::stod(requireValue("--scroll-percent"));
        } else if (arg == "--initial-scroll-percent") {
            opt.initialScrollPercent = std::stod(requireValue("--initial-scroll-percent"));
        } else if (arg == "--initial-wheel-units") {
            opt.initialWheelUnits = std::stoi(requireValue("--initial-wheel-units"));
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
    if ((command == "capture-window" || command == "capture-scroll") && opt.outputPath.empty()) {
        throw std::runtime_error("--out is required");
    }
    if (command == "restore-scroll" && (opt.scrollPercent < 0 || opt.scrollPercent > 100)) {
        throw std::runtime_error("--scroll-percent must be between 0 and 100");
    }
    if (opt.initialScrollPercent > 100) {
        throw std::runtime_error("--initial-scroll-percent must be between 0 and 100");
    }
    if (opt.initialWheelUnits < 0 || opt.initialWheelUnits > WHEEL_DELTA * 64) {
        throw std::runtime_error("--initial-wheel-units must be between 0 and 7680");
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

    session.IsCursorCaptureEnabled(false);
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

class WindowFrameCapturer {
public:
	struct CapturedFrame {
		Bitmap bitmap;
		uint64_t sequence = 0;
	};
    WindowFrameCapturer(ID3D11Device* d3dDevice, WGD3D::IDirect3DDevice const& winrtDevice, WGC::GraphicsCaptureItem const& item)
        : d3dDevice_(d3dDevice) {
        auto size = item.Size();
        if (size.Width <= 0 || size.Height <= 0) {
            throw std::runtime_error("GraphicsCaptureItem has an empty size");
        }
        framePool_ = WGC::Direct3D11CaptureFramePool::CreateFreeThreaded(
            winrtDevice,
            WGD::DirectXPixelFormat::B8G8R8A8UIntNormalized,
            2,
            size);
        session_ = framePool_.CreateCaptureSession(item);
        session_.IsCursorCaptureEnabled(false);
        token_ = framePool_.FrameArrived([this](WGC::Direct3D11CaptureFramePool const& sender, winrt::Windows::Foundation::IInspectable const&) {
            auto frame = sender.TryGetNextFrame();
            if (!frame) {
                return;
            }
            {
                std::lock_guard<std::mutex> lock(mutex_);
                latest_ = frame;
                ++sequence_;
            }
            cv_.notify_one();
        });
        session_.StartCapture();
    }

    ~WindowFrameCapturer() {
        if (framePool_) {
            framePool_.FrameArrived(token_);
        }
        if (session_) {
            session_.Close();
        }
        if (framePool_) {
            framePool_.Close();
        }
    }

    Bitmap capture(int timeoutMs) {
        WGC::Direct3D11CaptureFrame frame{nullptr};
        {
            std::unique_lock<std::mutex> lock(mutex_);
            int waitMs = latest_ ? (std::min)(timeoutMs, 200) : timeoutMs;
            cv_.wait_for(lock, std::chrono::milliseconds(waitMs), [&] { return sequence_ > consumedSequence_; });
            frame = latest_;
            consumedSequence_ = sequence_;
        }
        if (!frame) {
            throw std::runtime_error("Timed out waiting for a WGC frame");
        }
        winrt::com_ptr<ID3D11Texture2D> texture = textureFromSurface(frame.Surface());
        return bitmapFromTexture(d3dDevice_, texture.get(), frame.ContentSize());
    }

	CapturedFrame captureInitial(int timeoutMs) {
		return captureFreshAfter(0, timeoutMs).value_or(CapturedFrame{});
	}

	std::optional<CapturedFrame> captureFreshAfter(uint64_t afterSequence, int timeoutMs) {
		WGC::Direct3D11CaptureFrame frame{nullptr};
		uint64_t sequence = 0;
		{
			std::unique_lock<std::mutex> lock(mutex_);
			if (!cv_.wait_for(lock, std::chrono::milliseconds(timeoutMs), [&] { return sequence_ > afterSequence; })) {
				return std::nullopt;
			}
			frame = latest_;
			sequence = sequence_;
		}
		if (!frame) {
			return std::nullopt;
		}
		winrt::com_ptr<ID3D11Texture2D> texture = textureFromSurface(frame.Surface());
		return CapturedFrame{bitmapFromTexture(d3dDevice_, texture.get(), frame.ContentSize()), sequence};
	}

private:
    ID3D11Device* d3dDevice_{};
    WGC::Direct3D11CaptureFramePool framePool_{nullptr};
    WGC::GraphicsCaptureSession session_{nullptr};
    winrt::event_token token_{};
    std::mutex mutex_;
    std::condition_variable cv_;
    WGC::Direct3D11CaptureFrame latest_{nullptr};
    uint64_t sequence_ = 0;
    uint64_t consumedSequence_ = 0;
};

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

winrt::com_ptr<IUIAutomationScrollPattern> findScrollPatternAtPoint(HWND hwnd, int wheelX, int wheelY) {
    if (wheelX == std::numeric_limits<int>::min() || wheelY == std::numeric_limits<int>::min()) {
        return findScrollPattern(hwnd);
    }
    winrt::com_ptr<IUIAutomation> automation;
    winrt::check_hresult(CoCreateInstance(CLSID_CUIAutomation, nullptr, CLSCTX_INPROC_SERVER, IID_PPV_ARGS(automation.put())));
    POINT point{static_cast<LONG>(wheelX), static_cast<LONG>(wheelY)};
    winrt::com_ptr<IUIAutomationElement> element;
    if (FAILED(automation->ElementFromPoint(point, element.put())) || !element) {
        return findScrollPattern(hwnd);
    }
    winrt::com_ptr<IUIAutomationTreeWalker> walker;
    automation->get_ControlViewWalker(walker.put());
    for (int depth = 0; depth < 32 && element; ++depth) {
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
    return findScrollPattern(hwnd);
}

CaptureResult queryScrollPosition(Options const& opt) {
    CaptureResult result;
    result.method = "uia-scroll-position";
    auto pattern = findScrollPatternAtPoint(opt.hwnd, opt.wheelX, opt.wheelY);
    if (!pattern || FAILED(pattern->get_CurrentVerticalScrollPercent(&result.scrollPercent)) || result.scrollPercent < 0) {
        result.errorCode = "SCROLL_POSITION_UNAVAILABLE";
        result.message = "The selected target does not expose a vertical UI Automation scroll position.";
        return result;
    }
    result.ok = true;
    return result;
}

CaptureResult restoreScrollPosition(Options const& opt) {
    CaptureResult result;
    result.method = "uia-scroll-position";
    auto pattern = findScrollPatternAtPoint(opt.hwnd, opt.wheelX, opt.wheelY);
    if (!pattern || FAILED(pattern->SetScrollPercent(-1.0, opt.scrollPercent))) {
        result.errorCode = "SCROLL_RESTORE_FAILED";
        result.message = "Could not restore the selected UI Automation scroll position.";
        return result;
    }
    std::this_thread::sleep_for(std::chrono::milliseconds(120));
    double restored = -1;
    if (FAILED(pattern->get_CurrentVerticalScrollPercent(&restored)) || std::abs(restored - opt.scrollPercent) > 0.1) {
        result.errorCode = "SCROLL_RESTORE_FAILED";
        result.message = "The selected scroll position did not return to its origin.";
        return result;
    }
    result.ok = true;
    result.scrollPercent = restored;
    return result;
}

struct ScrollPositionGuard {
    IUIAutomationScrollPattern* pattern{};
    double percent = -1;
    bool restored = false;

    ~ScrollPositionGuard() {
        restore();
    }

    bool restore() {
        if (!restored && pattern && percent >= 0) {
            if (FAILED(pattern->SetScrollPercent(-1.0, percent))) {
                return false;
            }
            restored = true;
        }
        return true;
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

struct ViewportMask {
    int width = 0;
    int height = 0;
    std::vector<uint8_t> fixed;

    bool at(int x, int y) const {
        return x >= 0 && y >= 0 && x < width && y < height && fixed[static_cast<size_t>(y) * width + x] != 0;
    }
};

int pixelColorDifference(Bitmap const& a, int ax, int ay, Bitmap const& b, int bx, int by) {
    auto const* pa = a.bgra.data() + (static_cast<size_t>(ay) * a.width + ax) * 4;
    auto const* pb = b.bgra.data() + (static_cast<size_t>(by) * b.width + bx) * 4;
    return (std::abs(static_cast<int>(pa[0]) - static_cast<int>(pb[0])) +
        std::abs(static_cast<int>(pa[1]) - static_cast<int>(pb[1])) +
        std::abs(static_cast<int>(pa[2]) - static_cast<int>(pb[2]))) / 3;
}

ViewportMask detectViewportFixedMask(std::vector<Bitmap> const& frames, std::vector<int> const& origins) {
    ViewportMask mask;
    if (frames.size() < 2 || origins.size() != frames.size()) {
        return mask;
    }
    mask.width = frames.front().width;
    mask.height = frames.front().height;
    mask.fixed.assign(static_cast<size_t>(mask.width) * mask.height, 0);
    constexpr int blockSize = 2;
    for (int blockY = 0; blockY < mask.height; blockY += blockSize) {
        for (int blockX = 0; blockX < mask.width; blockX += blockSize) {
            int eligible = 0;
            int stableVotes = 0;
            int dynamicEligible = 0;
            int dynamicVotes = 0;
            for (size_t frameIndex = 1; frameIndex < frames.size(); ++frameIndex) {
                Bitmap const& previous = frames[frameIndex - 1];
                Bitmap const& current = frames[frameIndex];
                int movement = origins[frameIndex] - origins[frameIndex - 1];
                if (movement <= 1 || previous.width != mask.width || current.width != mask.width || previous.height != mask.height || current.height != mask.height) {
                    continue;
                }
                int64_t sameTotal = 0;
                int sameCount = 0;
                int64_t translatedTotal = 0;
                int translatedCount = 0;
                int dualDirectionCount = 0;
                int64_t forwardTotal = 0;
                int forwardCount = 0;
                int64_t backwardTotal = 0;
                int backwardCount = 0;
                int blockBottom = (std::min)(mask.height, blockY + blockSize);
                int blockRight = (std::min)(mask.width, blockX + blockSize);
                for (int y = blockY; y < blockBottom; ++y) {
                    for (int x = blockX; x < blockRight; ++x) {
                        sameTotal += pixelColorDifference(previous, x, y, current, x, y);
                        ++sameCount;
                        if (y + movement < mask.height && y >= movement) {
                            ++dualDirectionCount;
                        }
                        if (y + movement < mask.height) {
                            int difference = pixelColorDifference(previous, x, y + movement, current, x, y);
                            translatedTotal += difference;
                            ++translatedCount;
                            forwardTotal += difference;
                            ++forwardCount;
                        }
                        if (y >= movement) {
                            int difference = pixelColorDifference(previous, x, y, current, x, y - movement);
                            translatedTotal += difference;
                            ++translatedCount;
                            backwardTotal += difference;
                            ++backwardCount;
                        }
                    }
                }
                if (sameCount == 0 || translatedCount == 0) {
                    continue;
                }
                ++eligible;
                double same = static_cast<double>(sameTotal) / sameCount;
                double translated = static_cast<double>(translatedTotal) / translatedCount;
                if (same <= 6.0 && translated >= 14.0 && translated - same >= 8.0) {
                    ++stableVotes;
                }
                if (dualDirectionCount > 0) {
                    ++dynamicEligible;
                    double forward = forwardCount > 0 ? static_cast<double>(forwardTotal) / forwardCount : 0.0;
                    double backward = backwardCount > 0 ? static_cast<double>(backwardTotal) / backwardCount : 0.0;
                    if (same >= 8.0 && forward >= 18.0 && backward >= 18.0) {
                        ++dynamicVotes;
                    }
                }
            }
            if (eligible == 0) {
                continue;
            }
            int required = (eligible * 2 + 2) / 3;
            int dynamicRequired = (dynamicEligible * 3 + 3) / 4;
            bool dynamicFixed = frames.size() >= 4 && dynamicEligible >= 3 && dynamicVotes >= dynamicRequired;
            bool fixed = stableVotes >= required || dynamicFixed;
            if (!fixed) {
                continue;
            }
            for (int y = blockY; y < (std::min)(mask.height, blockY + blockSize); ++y) {
                for (int x = blockX; x < (std::min)(mask.width, blockX + blockSize); ++x) {
                    mask.fixed[static_cast<size_t>(y) * mask.width + x] = 1;
                }
            }
        }
    }

    std::vector<uint8_t> visited(mask.fixed.size(), 0);
    std::vector<uint8_t> expanded = mask.fixed;
    for (int startY = 0; startY < mask.height; ++startY) {
        for (int startX = 0; startX < mask.width; ++startX) {
            size_t startIndex = static_cast<size_t>(startY) * mask.width + startX;
            if (!mask.fixed[startIndex] || visited[startIndex]) {
                continue;
            }
            std::vector<std::pair<int, int>> queue{{startX, startY}};
            visited[startIndex] = 1;
            int left = startX;
            int right = startX + 1;
            int top = startY;
            int bottom = startY + 1;
            size_t cursor = 0;
            while (cursor < queue.size()) {
                auto [x, y] = queue[cursor++];
                left = (std::min)(left, x);
                right = (std::max)(right, x + 1);
                top = (std::min)(top, y);
                bottom = (std::max)(bottom, y + 1);
                constexpr int directions[4][2] = {{-1, 0}, {1, 0}, {0, -1}, {0, 1}};
                for (auto const& direction : directions) {
                    int nx = x + direction[0];
                    int ny = y + direction[1];
                    if (nx < 0 || ny < 0 || nx >= mask.width || ny >= mask.height) {
                        continue;
                    }
                    size_t index = static_cast<size_t>(ny) * mask.width + nx;
                    if (!mask.fixed[index] || visited[index]) {
                        continue;
                    }
                    visited[index] = 1;
                    queue.emplace_back(nx, ny);
                }
            }
            int boxWidth = right - left;
            int boxHeight = bottom - top;
            bool touchesEdge = top == 0 || bottom == mask.height;
            if (queue.size() < 8 && !touchesEdge) {
                for (auto const& [x, y] : queue) {
                    expanded[static_cast<size_t>(y) * mask.width + x] = 0;
                }
                continue;
            }
            int topEdgePixels = 0;
            int bottomEdgePixels = 0;
            int leftEdgePixels = 0;
            int rightEdgePixels = 0;
            for (auto const& [x, y] : queue) {
                topEdgePixels += y == top ? 1 : 0;
                bottomEdgePixels += y == bottom - 1 ? 1 : 0;
                leftEdgePixels += x == left ? 1 : 0;
                rightEdgePixels += x == right - 1 ? 1 : 0;
            }
            bool enclosingBorder = boxWidth >= 8 && boxHeight >= 8 &&
                topEdgePixels * 2 >= boxWidth && bottomEdgePixels * 2 >= boxWidth &&
                leftEdgePixels * 2 >= boxHeight && rightEdgePixels * 2 >= boxHeight;
            if (enclosingBorder) {
                left = (std::max)(0, left - 1);
                right = (std::min)(mask.width, right + 1);
                top = (std::max)(0, top - 1);
                bottom = (std::min)(mask.height, bottom + 1);
                for (int y = top; y < bottom; ++y) {
                    for (int x = left; x < right; ++x) {
                        expanded[static_cast<size_t>(y) * mask.width + x] = 1;
                    }
                }
            } else {
                for (auto const& [x, y] : queue) {
                    int expandLeft = (std::max)(0, x - 1);
                    int expandRight = (std::min)(mask.width, x + 2);
                    int expandTop = (std::max)(0, y - 1);
                    int expandBottom = (std::min)(mask.height, y + 2);
                    for (int ey = expandTop; ey < expandBottom; ++ey) {
                        for (int ex = expandLeft; ex < expandRight; ++ex) {
                            expanded[static_cast<size_t>(ey) * mask.width + ex] = 1;
                        }
                    }
                }
            }
        }
    }
    mask.fixed = std::move(expanded);
    return mask;
}

int fixedEdgeSpan(ViewportMask const& mask, bool fromTop) {
    if (mask.width <= 0 || mask.height <= 0 || mask.fixed.empty()) {
        return 0;
    }
    int span = 0;
    for (int offset = 0; offset < mask.height * 2 / 3; ++offset) {
        int y = fromTop ? offset : mask.height - 1 - offset;
        int fixedPixels = 0;
        for (int x = 0; x < mask.width; ++x) {
            fixedPixels += mask.at(x, y) ? 1 : 0;
        }
        if (fixedPixels * 2 < mask.width) {
            break;
        }
        ++span;
    }
    return span;
}

Bitmap composeDocumentFrames(std::vector<Bitmap> const& frames, std::vector<int> const& origins, ViewportMask const& mask) {
    if (frames.empty() || origins.size() != frames.size()) {
        throw std::runtime_error("invalid document frame composition");
    }
    Bitmap output;
    output.width = frames.front().width;
    output.height = origins.back() + frames.back().height;
    if (output.width <= 0 || output.height <= 0) {
        throw std::runtime_error("invalid stitched document dimensions");
    }
    constexpr size_t maxStitchedBytes = 256ull * 1024ull * 1024ull;
    size_t outputBytes = static_cast<size_t>(output.width) * output.height * 4;
    if (outputBytes > maxStitchedBytes) {
        throw std::runtime_error("stitched document exceeds memory budget");
    }
    output.bgra.assign(outputBytes, 0);
    auto copyPixel = [&](Bitmap const& frame, int x, int sourceY, int documentY) {
        size_t outputIndex = static_cast<size_t>(documentY) * output.width + x;
        auto const* source = frame.bgra.data() + (static_cast<size_t>(sourceY) * frame.width + x) * 4;
        auto* destination = output.bgra.data() + outputIndex * 4;
        std::copy(source, source + 4, destination);
    };

    int stickyTop = fixedEdgeSpan(mask, true);
    int stickyBottom = fixedEdgeSpan(mask, false);

    for (int documentY = 0; documentY < output.height; ++documentY) {
        std::array<int, maxScrollFrames> coveringFrames{};
        std::array<int, maxScrollFrames> coveringRows{};
        std::array<uint8_t, 32768> colorCounts{};
        int coveringCount = 0;
        for (size_t frameIndex = 0; frameIndex < frames.size() && coveringCount < static_cast<int>(coveringFrames.size()); ++frameIndex) {
            int sourceY = documentY - origins[frameIndex];
            if (sourceY >= 0 && sourceY < frames[frameIndex].height) {
                coveringFrames[coveringCount] = static_cast<int>(frameIndex);
                coveringRows[coveringCount] = sourceY;
                ++coveringCount;
            }
        }
        for (int x = 0; x < output.width; ++x) {
            std::array<int, maxScrollFrames> candidateFrames{};
            std::array<int, maxScrollFrames> candidateRows{};
            int candidateCount = 0;
            for (int covering = 0; covering < coveringCount; ++covering) {
                int frameIndex = coveringFrames[covering];
                int sourceY = coveringRows[covering];
                if (!mask.fixed.empty() && mask.at(x, sourceY)) {
                    continue;
                }
                candidateFrames[candidateCount] = frameIndex;
                candidateRows[candidateCount] = sourceY;
                ++candidateCount;
            }
            if (candidateCount == 0) {
                if (coveringCount > 0) {
                    candidateFrames[0] = coveringFrames[0];
                    candidateRows[0] = coveringRows[0];
                    candidateCount = 1;
                }
            }
            if (candidateCount == 0) {
                continue;
            }
            std::array<uint16_t, maxScrollFrames> touchedColors{};
            int touchedCount = 0;
            int bestColorCount = 0;
            uint16_t bestColor = 0;
            for (int candidate = 0; candidate < candidateCount; ++candidate) {
                Bitmap const& frame = frames[static_cast<size_t>(candidateFrames[candidate])];
                auto const* pixel = frame.bgra.data() + (static_cast<size_t>(candidateRows[candidate]) * frame.width + x) * 4;
                uint16_t key = static_cast<uint16_t>(((pixel[0] >> 3) << 10) | ((pixel[1] >> 3) << 5) | (pixel[2] >> 3));
                if (colorCounts[key] == 0) {
                    touchedColors[touchedCount++] = key;
                }
                int count = ++colorCounts[key];
                if (count > bestColorCount) {
                    bestColorCount = count;
                    bestColor = key;
                }
            }
            int best = -1;
            bool useColorConsensus = bestColorCount >= 2;
            int bestCenterDistance = std::numeric_limits<int>::max();
            for (int candidate = 0; candidate < candidateCount; ++candidate) {
                Bitmap const& candidateFrame = frames[static_cast<size_t>(candidateFrames[candidate])];
                auto const* pixel = candidateFrame.bgra.data() + (static_cast<size_t>(candidateRows[candidate]) * candidateFrame.width + x) * 4;
                uint16_t key = static_cast<uint16_t>(((pixel[0] >> 3) << 10) | ((pixel[1] >> 3) << 5) | (pixel[2] >> 3));
                if (useColorConsensus && key != bestColor) {
                    continue;
                }
                int centerDistance = std::abs(candidateRows[candidate] - candidateFrame.height / 2);
                if (centerDistance < bestCenterDistance) {
                    best = candidate;
                    bestCenterDistance = centerDistance;
                }
            }
            for (int touched = 0; touched < touchedCount; ++touched) {
                colorCounts[touchedColors[touched]] = 0;
            }
            if (best < 0) {
                best = 0;
            }
            int frameIndex = candidateFrames[best];
            copyPixel(frames[static_cast<size_t>(frameIndex)], x, candidateRows[best], documentY);
        }
    }

    for (int y = 0; y < stickyTop; ++y) {
        for (int x = 0; x < output.width; ++x) {
            if (mask.at(x, y)) {
                copyPixel(frames.front(), x, y, y);
            }
        }
    }
    for (int offset = 0; offset < stickyBottom; ++offset) {
        int sourceY = frames.back().height - stickyBottom + offset;
        int documentY = output.height - stickyBottom + offset;
        for (int x = 0; x < output.width; ++x) {
            if (mask.at(x, sourceY)) {
                copyPixel(frames.back(), x, sourceY, documentY);
            }
        }
    }
    return output;
}

Bitmap stitchVertical(std::vector<Bitmap> const& frames, int overlapPx, std::vector<int> const& estimatedSkipTops = {}, std::vector<int>* frameOffsets = nullptr, bool requireMeasured = false, bool dimensionsOnly = false, int composeX = -1, int composeWidth = 0) {
    if (frames.empty()) {
        throw std::runtime_error("no frames captured for scrolling screenshot");
    }
    for (Bitmap const& frame : frames) {
        if (frame.empty() || frame.width != frames.front().width || frame.height != frames.front().height) {
            throw std::runtime_error("scroll frames changed dimensions");
        }
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

    ViewportMask const* scoreMask = nullptr;

    auto unchangedEdgeRows = [&](Bitmap const& prev, Bitmap const& next, bool fromTop, int limit, int width) -> int {
        int rows = 0;
        int colStep = (std::max)(1, width / 96);
        int height = (std::min)(prev.height, next.height);
        limit = (std::min)(limit, height * 2 / 3);
        for (int offset = 0; offset < limit; ++offset) {
            int y = fromTop ? offset : height - 1 - offset;
            double total = 0;
            int count = 0;
            for (int x = 0; x < width; x += colStep) {
                total += std::abs(pixelLuma(prev, x, y) - pixelLuma(next, x, y));
                ++count;
            }
            if (count == 0 || total / count > 2.0) {
                break;
            }
            ++rows;
        }
        return rows;
    };

    auto bandScore = [&](Bitmap const& prev, Bitmap const& next, int overlap, int width, int nextTopSkip, int prevBottomSkip) -> double {
        int nextStartY = nextTopSkip;
        int nextEndY = overlap - prevBottomSkip;
        int compareHeight = nextEndY - nextStartY;
        int prevStartY = prev.height - overlap + nextStartY;
        if (compareHeight < 12 || prevStartY < 0 || nextEndY > next.height) {
            return std::numeric_limits<double>::infinity();
        }
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
                if (scoreMask && (scoreMask->at(x, py) || scoreMask->at(x, ny))) {
                    continue;
                }
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

    auto searchBestSkip = [&](Bitmap const& prev, Bitmap const& next, int width, int searchMin, int searchMax, int nextTopSkip, int prevBottomSkip) -> StitchSearchResult {
        StitchSearchResult result;
        std::vector<std::pair<int, double>> candidates;
        for (int skipTop = searchMin; skipTop <= searchMax; ++skipTop) {
            double score = bandScore(prev, next, skipTop, width, nextTopSkip, prevBottomSkip);
            candidates.emplace_back(skipTop, score);
            if (score < result.score) {
                result.score = score;
                result.skipTop = skipTop;
            }
        }
        for (auto const& candidate : candidates) {
            if (std::abs(candidate.first - result.skipTop) >= 4 && candidate.second < result.alternativeScore) {
                result.alternativeScore = candidate.second;
            }
        }
        return result;
    };

    auto detectedSkipTop = [&](Bitmap const& prev, Bitmap const& next, int width, int estimate) -> StitchSearchResult {
        int fixedFallback = (std::min)(overlapPx, (std::max)(0, next.height - 1));
        int fallback = estimate >= 0 ? (std::min)((std::max)(0, estimate), (std::max)(0, next.height - 1)) : fixedFallback;
        if (width <= 0) {
            return StitchSearchResult{ fallback, std::numeric_limits<double>::infinity(), std::numeric_limits<double>::infinity(), false, 0, 0 };
        }
        int nextTopSkip = unchangedEdgeRows(prev, next, true, 96, width);
        int prevBottomSkip = unchangedEdgeRows(prev, next, false, 48, width);
        int fullMin = nextTopSkip + prevBottomSkip + 12;
        int fullMax = (std::min)(prev.height, next.height) - 1;
        if (fullMax < fullMin) {
            return StitchSearchResult{ fallback, std::numeric_limits<double>::infinity(), std::numeric_limits<double>::infinity(), false, nextTopSkip, prevBottomSkip };
        }

        auto confident = [&](StitchSearchResult const& candidate) {
            double minimumGap = (std::max)(0.5, candidate.score * 0.08);
            return candidate.score <= 10.0 &&
                (!std::isfinite(candidate.alternativeScore) || candidate.alternativeScore - candidate.score >= minimumGap);
        };
        if (estimate >= 0) {
            int radius = (std::max)(96, next.height / 8);
            int localMin = (std::max)(fullMin, fallback - radius);
            int localMax = (std::min)(fullMax, fallback + radius);
            StitchSearchResult local = searchBestSkip(prev, next, width, localMin, localMax, nextTopSkip, prevBottomSkip);
            if (confident(local)) {
                local.measured = true;
                local.stickyTop = nextTopSkip;
                local.stickyBottom = prevBottomSkip;
                return local;
            }
        }

        StitchSearchResult full = searchBestSkip(prev, next, width, fullMin, fullMax, nextTopSkip, prevBottomSkip);
        if (confident(full)) {
            full.measured = true;
            full.stickyTop = nextTopSkip;
            full.stickyBottom = prevBottomSkip;
            return full;
        }
        return StitchSearchResult{ fallback, full.score, full.alternativeScore, false, nextTopSkip, prevBottomSkip };
    };

    int width = frames.front().width;
    bool authoritativeEstimates = requireMeasured && estimatedSkipTops.size() == frames.size();
    for (size_t i = 1; authoritativeEstimates && i < estimatedSkipTops.size(); ++i) {
        authoritativeEstimates = estimatedSkipTops[i] >= 0;
    }
    std::vector<int> origins(frames.size(), 0);
    std::vector<int> measuredOverlaps;
    std::vector<int> stickyTopEvidence;
    std::vector<int> stickyBottomEvidence;
    for (size_t i = 1; i < frames.size(); ++i) {
        int historyEstimate = medianOfRecent(measuredOverlaps, 10);
        int estimate = i < estimatedSkipTops.size() ? estimatedSkipTops[i] : historyEstimate;
        StitchSearchResult detected = detectedSkipTop(frames[i - 1], frames[i], width, estimate);
        if (authoritativeEstimates) {
            detected.skipTop = (std::min)((std::max)(1, estimate), frames[i].height - 1);
            detected.measured = true;
        }
        if (requireMeasured && !detected.measured && estimate < 0) {
            throw std::runtime_error("ambiguous frame overlap");
        }
        if (detected.measured) {
            measuredOverlaps.push_back(detected.skipTop);
        } else if (historyEstimate >= 0 && estimate < 0) {
            detected.skipTop = (std::min)((std::max)(historyEstimate, 1), frames[i].height - 1);
        }
        int overlap = (std::min)((std::max)(1, detected.skipTop), frames[i].height - 1);
        stickyTopEvidence.push_back(detected.stickyTop);
        stickyBottomEvidence.push_back(detected.stickyBottom);
        origins[i] = origins[i - 1] + frames[i].height - overlap;
    }
    if (frameOffsets) {
        *frameOffsets = origins;
    }
    ViewportMask fixedMask = detectViewportFixedMask(frames, origins);
    auto applyFixedEdges = [&](ViewportMask& target) {
        int stickyTop = medianOfRecent(stickyTopEvidence, stickyTopEvidence.size());
        int stickyBottom = medianOfRecent(stickyBottomEvidence, stickyBottomEvidence.size());
        for (int y = 0; y < stickyTop && y < target.height; ++y) {
            std::fill(target.fixed.begin() + static_cast<size_t>(y) * target.width, target.fixed.begin() + static_cast<size_t>(y + 1) * target.width, uint8_t{1});
        }
        for (int offset = 0; offset < stickyBottom && offset < target.height; ++offset) {
            int y = target.height - 1 - offset;
            std::fill(target.fixed.begin() + static_cast<size_t>(y) * target.width, target.fixed.begin() + static_cast<size_t>(y + 1) * target.width, uint8_t{1});
        }
    };
    applyFixedEdges(fixedMask);
    scoreMask = authoritativeEstimates ? nullptr : &fixedMask;
    std::vector<int> refinedOrigins(frames.size(), 0);
    bool refinedAny = false;
    for (size_t i = 1; !authoritativeEstimates && i < frames.size(); ++i) {
        int provisionalMovement = origins[i] - origins[i - 1];
        int provisionalOverlap = frames[i].height - provisionalMovement;
        int searchMin = (std::max)(12, provisionalOverlap - 8);
        int searchMax = (std::min)(frames[i].height - 1, provisionalOverlap + 8);
        StitchSearchResult refined = searchBestSkip(frames[i - 1], frames[i], width, searchMin, searchMax, 0, 0);
        double minimumGap = (std::max)(0.5, refined.score * 0.08);
        bool confident = refined.score <= 10.0 &&
            (!std::isfinite(refined.alternativeScore) || refined.alternativeScore - refined.score >= minimumGap);
        int overlap = confident ? refined.skipTop : provisionalOverlap;
        refinedAny = refinedAny || overlap != provisionalOverlap;
        refinedOrigins[i] = refinedOrigins[i - 1] + frames[i].height - overlap;
    }
    scoreMask = nullptr;
    if (refinedAny) {
        origins = std::move(refinedOrigins);
        fixedMask = detectViewportFixedMask(frames, origins);
        applyFixedEdges(fixedMask);
        if (frameOffsets) {
            *frameOffsets = origins;
        }
    }
    if (dimensionsOnly) {
        Bitmap dimensions;
        dimensions.width = frames.front().width;
        dimensions.height = origins.back() + frames.back().height;
        return dimensions;
    }
    if (composeX >= 0 && composeWidth > 0 && composeX <= frames.front().width - composeWidth) {
        std::vector<Bitmap> croppedFrames;
        croppedFrames.reserve(frames.size());
        for (Bitmap const& frame : frames) {
            Bitmap cropped;
            cropped.width = composeWidth;
            cropped.height = frame.height;
            cropped.bgra.resize(static_cast<size_t>(cropped.width) * cropped.height * 4);
            for (int y = 0; y < cropped.height; ++y) {
                auto const* source = frame.bgra.data() + (static_cast<size_t>(y) * frame.width + composeX) * 4;
                auto* destination = cropped.bgra.data() + static_cast<size_t>(y) * cropped.width * 4;
                std::copy(source, source + static_cast<size_t>(cropped.width) * 4, destination);
            }
            croppedFrames.push_back(std::move(cropped));
        }
        ViewportMask croppedMask;
        croppedMask.width = composeWidth;
        croppedMask.height = frames.front().height;
        croppedMask.fixed.resize(static_cast<size_t>(croppedMask.width) * croppedMask.height);
        if (fixedMask.width == frames.front().width && fixedMask.height == frames.front().height && !fixedMask.fixed.empty()) {
            for (int y = 0; y < croppedMask.height; ++y) {
                auto const* source = fixedMask.fixed.data() + static_cast<size_t>(y) * fixedMask.width + composeX;
                auto* destination = croppedMask.fixed.data() + static_cast<size_t>(y) * croppedMask.width;
                std::copy(source, source + croppedMask.width, destination);
            }
        }
        return composeDocumentFrames(croppedFrames, origins, croppedMask);
    }
    return composeDocumentFrames(frames, origins, fixedMask);
}

Bitmap cropBitmap(Bitmap const& src, int x, int y, int width, int height) {
    if (src.empty() || width <= 0 || height <= 0) {
        throw std::runtime_error("invalid crop dimensions");
    }
    if (x < 0 || y < 0 || x > src.width - width || y > src.height - height) {
        throw std::runtime_error("crop exceeds stitched image bounds");
    }

    Bitmap out;
    out.width = width;
    out.height = height;
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

struct ViewportOffset {
    int pixels = 0;
    bool measured = false;
};

int measureDownwardViewportMovement(Bitmap const& before, Bitmap const& after, int overlapPx) {
    if (before.empty() || after.empty() || before.width != after.width || before.height != after.height) {
        return -1;
    }
    try {
        std::vector<int> offsets;
        (void)stitchVertical({before, after}, overlapPx, {}, &offsets, true, true);
        if (offsets.size() == 2 && offsets[1] > 0) {
            return offsets[1];
        }
    } catch (std::runtime_error const&) {
    }
    return -1;
}

ViewportOffset measureViewportOffset(Bitmap const& origin, Bitmap const& current, int overlapPx) {
    if (bitmapDifferenceScore(origin, current) < 0.5) {
        return ViewportOffset{0, true};
    }
    int downward = measureDownwardViewportMovement(origin, current, overlapPx);
    int upward = measureDownwardViewportMovement(current, origin, overlapPx);
    if (downward >= 0 && upward < 0) {
        return ViewportOffset{downward, true};
    }
    if (upward >= 0 && downward < 0) {
        return ViewportOffset{-upward, true};
    }
    return ViewportOffset{};
}

std::vector<int> rangeOverlapEstimates(std::vector<Bitmap> const& frames, std::vector<int> const& frameStepCounts, Options const& opt) {
    (void)frameStepCounts;
    (void)opt;
    std::vector<int> estimates(frames.size(), -1);
    return estimates;
}

int rangeCoverageSafety(int frameHeight);

int rangeStepBatch(Options const& opt, int frameHeight, int remainingSteps) {
    if (remainingSteps <= 0) {
        return 1;
    }
    if (opt.rangeDistancePx <= 0 || opt.rangeSteps <= 0 || frameHeight <= 0) {
        return 1;
    }
    int movement = (std::max)(1, static_cast<int>(std::round(static_cast<double>(opt.rangeDistancePx) / opt.rangeSteps)));
    int maxMovement = (std::max)(movement, frameHeight - rangeCoverageSafety(frameHeight));
    int batch = (std::max)(1, maxMovement / movement);
    return (std::min)(remainingSteps, (std::min)(2, batch));
}

std::vector<int> rangeWheelInputs(int batch) {
    return std::vector<int>(static_cast<size_t>((std::max)(0, batch)), -WHEEL_DELTA);
}

std::vector<int> initialRestoreWheelInputs(int wheelUnits) {
    std::vector<int> inputs;
    while (wheelUnits > 0) {
        int delta = (std::min)(WHEEL_DELTA, wheelUnits);
        inputs.push_back(delta);
        wheelUnits -= delta;
    }
    return inputs;
}

int rangeCoverageSafety(int frameHeight) {
    return (std::max)(48, (std::min)(160, frameHeight / 5));
}

bool rangeCoverageSatisfied(int stitchedHeight, int targetBottom, int frameHeight, bool includeSafety) {
    if (targetBottom <= 0 || stitchedHeight < targetBottom) {
        return false;
    }
    return !includeSafety || stitchedHeight >= targetBottom + rangeCoverageSafety(frameHeight);
}

bool controlledMovementUsable(int movement, int frameHeight) {
	return movement > 0 && frameHeight > 0 && movement <= frameHeight - rangeCoverageSafety(frameHeight);
}

int controlledUiaMovementEstimate(int frameHeight, double beforePercent, double afterPercent, double viewSize, int fallbackOverlap) {
	if (afterPercent <= beforePercent) {
		return -1;
	}
	int overlap = estimateSkipTopFromScroll(frameHeight, frameHeight, beforePercent, afterPercent, viewSize, fallbackOverlap);
	int movement = frameHeight - overlap;
	return controlledMovementUsable(movement, frameHeight) ? movement : -1;
}

std::vector<int> controlledOverlapEstimates(std::vector<Bitmap> const& frames, std::vector<int> const& movements) {
	std::vector<int> estimates(frames.size(), -1);
	for (size_t i = 1; i < frames.size() && i < movements.size(); ++i) {
		if (controlledMovementUsable(movements[i], frames[i - 1].height)) {
			estimates[i] = frames[i - 1].height - movements[i];
		}
	}
	return estimates;
}

bool rangeReplayComplete(int sentSteps, int requestedSteps) {
    return requestedSteps <= 0 || sentSteps >= requestedSteps;
}

int measuredRangeCropHeight(std::vector<int> const& frameOffsets, std::vector<int> const& frameStepCounts, std::vector<Bitmap> const& frames, Options const& opt) {
    if (opt.rangeEndY <= 0 || frameOffsets.empty() || frameOffsets.size() != frameStepCounts.size() || frameOffsets.size() != frames.size()) {
        return opt.cropH;
    }
    int cumulativeSteps = 0;
    size_t endpointFrame = frameOffsets.size() - 1;
    for (size_t frameIndex = 1; frameIndex < frameStepCounts.size(); ++frameIndex) {
        cumulativeSteps += frameStepCounts[frameIndex];
        endpointFrame = frameIndex;
        if (cumulativeSteps >= opt.rangeSteps) {
            break;
        }
    }
    int endpointY = frameOffsets[endpointFrame] + (std::min)(opt.rangeEndY, frames[endpointFrame].height);
    return endpointY - opt.cropY;
}

POINT wheelTargetPoint(RECT const& rect, int wheelX, int wheelY) {
    POINT target{(rect.left + rect.right) / 2, (rect.top + rect.bottom) / 2};
    if (wheelX != std::numeric_limits<int>::min() && wheelY != std::numeric_limits<int>::min()) {
        target.x = static_cast<LONG>((std::min)((std::max)(wheelX, static_cast<int>(rect.left)), static_cast<int>(rect.right - 1)));
        target.y = static_cast<LONG>((std::min)((std::max)(wheelY, static_cast<int>(rect.top)), static_cast<int>(rect.bottom - 1)));
    }
    return target;
}

bool sendWheelMessage(HWND hwnd, int delta, POINT point) {
    HWND target = WindowFromPoint(point);
    if (!target || GetAncestor(target, GA_ROOT) != GetAncestor(hwnd, GA_ROOT)) {
        target = hwnd;
    }
    DWORD_PTR ignored = 0;
    LRESULT sent = SendMessageTimeoutW(
        target,
        WM_MOUSEWHEEL,
        MAKEWPARAM(0, static_cast<short>(delta)),
        MAKELPARAM(static_cast<short>(point.x), static_cast<short>(point.y)),
        SMTO_ABORTIFHUNG,
        250,
        &ignored);
    return sent != 0;
}

bool sendWheelInput(HWND hwnd, int delta, int wheelX = std::numeric_limits<int>::min(), int wheelY = std::numeric_limits<int>::min()) {
    RECT rect{};
    if (!hwnd || !GetWindowRect(hwnd, &rect)) {
        return false;
    }
    POINT target = wheelTargetPoint(rect, wheelX, wheelY);
    SetForegroundWindow(hwnd);
    bool positioned = SetCursorPos(target.x, target.y) != FALSE;
    if (positioned) {
        INPUT input{};
        input.type = INPUT_MOUSE;
        input.mi.dwFlags = MOUSEEVENTF_WHEEL;
        input.mi.mouseData = static_cast<DWORD>(delta);
        if (SendInput(1, &input, sizeof(INPUT)) == 1) {
            std::this_thread::sleep_for(std::chrono::milliseconds(90));
            return true;
        }
    }
    bool sent = sendWheelMessage(hwnd, delta, target);
    if (sent) {
        std::this_thread::sleep_for(std::chrono::milliseconds(90));
    }
    return sent;
}

struct WheelCursorGuard {
    POINT original{};
    bool restore = false;

    ~WheelCursorGuard() {
        if (restore) {
            SetCursorPos(original.x, original.y);
        }
    }

    void position(HWND hwnd, int wheelX, int wheelY) {
        RECT rect{};
        if (!hwnd || !GetWindowRect(hwnd, &rect)) {
            return;
        }
        if (GetCursorPos(&original)) {
            restore = true;
        }
        POINT target = wheelTargetPoint(rect, wheelX, wheelY);
        SetCursorPos(target.x, target.y);
    }
};

struct WheelScrollGuard {
    HWND hwnd{};
    int wheelX = std::numeric_limits<int>::min();
    int wheelY = std::numeric_limits<int>::min();
    std::vector<int> forwardDeltas;
    std::vector<int> forwardMovements;
    Bitmap const* originFrame = nullptr;
    int overlapPx = 96;
    std::function<bool(int)> sendDelta;
    std::function<Bitmap()> captureViewport;
    bool restored = false;
    bool restorationOK = true;

    ~WheelScrollGuard() {
        try {
            restore();
        } catch (...) {
            restorationOK = false;
        }
    }

    std::vector<int> pendingRestoreDeltas() const {
        std::vector<int> deltas;
        deltas.reserve(forwardDeltas.size());
        for (auto it = forwardDeltas.rbegin(); it != forwardDeltas.rend(); ++it) {
            deltas.push_back(-*it);
        }
        return deltas;
    }

    bool restore() {
        if (restored || !hwnd || forwardDeltas.empty()) {
            restored = true;
            return restorationOK;
        }
        if (originFrame && captureViewport && sendDelta) {
            restorationOK = restoreMeasured();
            restored = true;
            return restorationOK;
        }
        for (int delta : pendingRestoreDeltas()) {
            if (!sendWheelInput(hwnd, delta, wheelX, wheelY)) {
                restorationOK = false;
                break;
            }
        }
        std::this_thread::sleep_for(std::chrono::milliseconds(120));
        restored = true;
        return restorationOK;
    }

    void record(int delta) {
        forwardDeltas.push_back(delta);
        forwardMovements.push_back(0);
    }

    bool recordIfMoved(int delta, Bitmap const& before, Bitmap const& after) {
        int movement = measureDownwardViewportMovement(before, after, overlapPx);
        if (movement <= 0 && bitmapDifferenceScore(before, after) < 0.5) {
            return false;
        }
        forwardDeltas.push_back(delta);
        forwardMovements.push_back((std::max)(0, movement));
        return true;
    }

    void discardLast(int count = 1) {
        while (count-- > 0 && !forwardDeltas.empty()) {
            forwardDeltas.pop_back();
            if (!forwardMovements.empty()) {
                forwardMovements.pop_back();
            }
        }
    }

    bool restoreMeasured() {
        int stepEstimate = 0;
        std::vector<int> measured;
        for (int movement : forwardMovements) {
            if (movement > 0) {
                measured.push_back(movement);
            }
        }
        if (!measured.empty()) {
            stepEstimate = medianOfRecent(measured, measured.size());
        }
        if (stepEstimate <= 0) {
            stepEstimate = 360;
        }

        Bitmap current = captureViewport();
        size_t fallbackIndex = forwardDeltas.size();
        int maxAttempts = (std::min)(96, (std::max)(12, static_cast<int>(forwardDeltas.size()) * 4 + 8));
        for (int attempt = 0; attempt < maxAttempts; ++attempt) {
            ViewportOffset before = measureViewportOffset(*originFrame, current, overlapPx);
            if (before.measured && before.pixels == 0) {
                forwardDeltas.clear();
                forwardMovements.clear();
                return true;
            }

            int delta = 0;
            if (before.measured) {
                int magnitude = (std::min)(WHEEL_DELTA, (std::max)(1, static_cast<int>(std::round(
                    static_cast<double>(WHEEL_DELTA) * std::abs(before.pixels) / stepEstimate))));
                delta = before.pixels > 0 ? magnitude : -magnitude;
            } else if (fallbackIndex > 0) {
                delta = -forwardDeltas[--fallbackIndex];
            } else {
                return false;
            }
            if (!sendDelta(delta)) {
                return false;
            }
            Bitmap next = captureViewport();
            ViewportOffset after = measureViewportOffset(*originFrame, next, overlapPx);
            if (before.measured && after.measured && before.pixels != after.pixels) {
                int observed = std::abs(after.pixels - before.pixels);
                stepEstimate = (std::max)(1, observed * WHEEL_DELTA / (std::max)(1, std::abs(delta)));
            }
            current = std::move(next);
        }
        ViewportOffset finalOffset = measureViewportOffset(*originFrame, current, overlapPx);
        return finalOffset.measured && finalOffset.pixels == 0;
    }
};

Bitmap storedScrollFrame(Options const& opt, Bitmap const& frame) {
    (void)opt;
    return frame;
}

struct ControlledStepResult {
	bool moved = false;
	bool settled = false;
	bool visuallyChanged = false;
	int movement = 0;
	WindowFrameCapturer::CapturedFrame frame;
};

struct ControlledLoopPolicyState {
	int consecutiveNoMotion = 0;
	int acceptedFrames = 1;
	bool finished = false;
};

enum class ControlledObservation {
	MovedSettled,
	NoMotion,
	UiaBottom,
	SettleTimeout,
};

bool applyControlledObservation(ControlledLoopPolicyState& state, ControlledObservation observation) {
	if (state.finished) {
		return false;
	}
	switch (observation) {
	case ControlledObservation::MovedSettled:
		state.consecutiveNoMotion = 0;
		++state.acceptedFrames;
		return true;
	case ControlledObservation::NoMotion:
		state.finished = ++state.consecutiveNoMotion >= 2;
		return false;
	case ControlledObservation::UiaBottom:
	case ControlledObservation::SettleTimeout:
		state.finished = true;
		return false;
	}
	return false;
}

template <typename Restore, typename Compose>
bool restoreBeforeCompose(Restore restore, Compose compose) {
	if (!restore()) {
		return false;
	}
	compose();
	return true;
}

template <typename PrimaryRestore, typename FallbackRestore>
bool restoreWithFallback(PrimaryRestore primary, FallbackRestore fallback) {
	return primary() || fallback();
}

bool controlledMovementPositionStable(int previousMovement, int currentMovement) {
	return previousMovement > 0 && currentMovement > 0 && std::abs(previousMovement - currentMovement) <= 2;
}

bool controlledQuietPeriodSettles(bool moved, bool hasAcceptedCandidate) {
	return moved && hasAcceptedCandidate;
}

Bitmap controlledMeasurementBitmap(Bitmap const& frame, Options const& opt) {
	if (opt.cropW > 0 && opt.cropH > 0 && opt.cropX >= 0 && opt.cropY >= 0 &&
		opt.cropX+opt.cropW <= frame.width && opt.cropY+opt.cropH <= frame.height) {
		return cropBitmap(frame, opt.cropX, opt.cropY, opt.cropW, opt.cropH);
	}
	return frame;
}

bool controlledStepCanBeAccepted(bool moved, bool settled, bool hasCandidate) {
	(void)settled;
	return moved && hasCandidate;
}

ControlledStepResult captureControlledStep(WindowFrameCapturer& capturer, Bitmap const& baseline, uint64_t& sequence, Options const& opt) {
	ControlledStepResult result;
	Bitmap previousFresh;
	Bitmap baselineMeasurement = controlledMeasurementBitmap(baseline, opt);
	int stableCount = 0;
	int previousMovement = -1;
	auto deadline = std::chrono::steady_clock::now() + std::chrono::milliseconds((std::max)(700, opt.delayMs * 6));
	while (std::chrono::steady_clock::now() < deadline) {
		auto fresh = capturer.captureFreshAfter(sequence, (std::min)(180, opt.timeoutMs));
		if (!fresh) {
			if (controlledQuietPeriodSettles(result.moved, !result.frame.bitmap.empty())) {
				result.settled = true;
				return result;
			}
			continue;
		}
		sequence = fresh->sequence;
		Bitmap freshMeasurement = controlledMeasurementBitmap(fresh->bitmap, opt);
		if (bitmapDifferenceScore(baselineMeasurement, freshMeasurement) >= 0.5) {
			result.visuallyChanged = true;
		}
		int movement = measureDownwardViewportMovement(baselineMeasurement, freshMeasurement, opt.overlapPx);
		if (controlledMovementUsable(movement, baselineMeasurement.height)) {
			result.moved = true;
			result.movement = movement;
		}
		bool pixelsStable = result.moved && !previousFresh.empty() && bitmapDifferenceScore(previousFresh, freshMeasurement) < 0.5;
		bool positionStable = controlledMovementPositionStable(previousMovement, movement);
		if (pixelsStable || positionStable) {
			++stableCount;
			if (stableCount >= 2) {
				result.settled = true;
				result.frame = std::move(*fresh);
				return result;
			}
		} else if (result.moved) {
			stableCount = 0;
		}
		if (movement > 0) {
			previousMovement = movement;
		}
		previousFresh = std::move(freshMeasurement);
		result.frame = std::move(*fresh);
	}
	return result;
}

bool scrollPatternAtBottom(IUIAutomationScrollPattern* pattern) {
	if (!pattern) {
		return false;
	}
	double percent = -1;
	return SUCCEEDED(pattern->get_CurrentVerticalScrollPercent(&percent)) && percent >= 99.9;
}

bool readScrollMetrics(IUIAutomationScrollPattern* pattern, double& percent, double& viewSize) {
	percent = -1;
	viewSize = -1;
	return pattern && SUCCEEDED(pattern->get_CurrentVerticalScrollPercent(&percent)) &&
		SUCCEEDED(pattern->get_CurrentVerticalViewSize(&viewSize)) && percent >= 0 && viewSize > 0 && viewSize < 100;
}

bool controlledNoMotionEnded(int consecutiveNoMotion) {
	return consecutiveNoMotion >= 2;
}

int controlledSelectionCropHeight(int stitchedHeight, int frameHeight, Options const& opt, bool pageEndConfirmed) {
	int bottomInset = pageEndConfirmed ? 0 : (std::max)(0, frameHeight - (opt.cropY + opt.cropH));
	return stitchedHeight - opt.cropY - bottomInset;
}

CaptureResult captureScrollWithWheelFallback(Options const& opt, ID3D11Device* d3dDevice, WGD3D::IDirect3DDevice const& winrtDevice, WGC::GraphicsCaptureItem const& item) {
    CaptureResult result;
    result.method = "wgc-wheel-scroll";
    result.outputPath = opt.outputPath;

    WheelCursorGuard cursor;
    cursor.position(opt.hwnd, opt.wheelX, opt.wheelY);

	winrt::com_ptr<IUIAutomationScrollPattern> initialPattern = findScrollPatternAtPoint(opt.hwnd, opt.wheelX, opt.wheelY);
	double initialPercent = -1;
	if (initialPattern) {
		initialPattern->get_CurrentVerticalScrollPercent(&initialPercent);
	}
	if (opt.initialScrollPercent >= 0) {
		initialPercent = opt.initialScrollPercent;
		if (!initialPattern || FAILED(initialPattern->SetScrollPercent(-1.0, initialPercent))) {
			result.errorCode = "SCROLL_START_FAILED";
			result.message = "Could not move the selected target to its saved starting position.";
			return result;
		}
		std::this_thread::sleep_for(std::chrono::milliseconds(120));
	}

    std::vector<Bitmap> frames;
    frames.reserve(static_cast<size_t>(opt.maxFrames));
    WindowFrameCapturer frameCapturer(d3dDevice, winrtDevice, item);
	auto initialCaptured = frameCapturer.captureInitial(opt.timeoutMs);
	if (initialCaptured.bitmap.empty()) {
		result.errorCode = "WGC_FRAME_TIMEOUT";
		result.message = "Timed out waiting for the initial scroll capture frame.";
		return result;
	}
	uint64_t sequence = initialCaptured.sequence;
	Bitmap initialFullFrame = std::move(initialCaptured.bitmap);
	frames.push_back(storedScrollFrame(opt, initialFullFrame));
	std::vector<int> frameStepCounts{ 0 };
	std::vector<int> acceptedMovements{0};

	int targetActions = opt.maxFrames - 1;
    constexpr size_t maxBufferedFrameBytes = 288ull * 1024ull * 1024ull;
    size_t frameBytes = frames.front().bgra.size();
    int memoryFrameLimit = frameBytes == 0 ? 1 : static_cast<int>((std::max)(size_t{ 1 }, maxBufferedFrameBytes / frameBytes));
    targetActions = (std::min)(targetActions, (std::max)(0, memoryFrameLimit - 1));
	bool pageEndConfirmed = false;
	ControlledLoopPolicyState loopState;
	WheelScrollGuard restore{ opt.hwnd, opt.wheelX, opt.wheelY };
	restore.originFrame = &frames.front();
    restore.overlapPx = opt.overlapPx;
    restore.sendDelta = [&](int delta) {
        return sendWheelInput(opt.hwnd, delta, opt.wheelX, opt.wheelY);
    };
	restore.captureViewport = [&]() { return frameCapturer.capture(opt.timeoutMs); };
	auto restoreOrigin = [&]() {
		if (initialPattern && initialPercent >= 0) {
			auto restoreUIA = [&]() {
				if (FAILED(initialPattern->SetScrollPercent(-1.0, initialPercent))) {
					return false;
				}
				std::this_thread::sleep_for(std::chrono::milliseconds(180));
				double restoredPercent = -1;
				return SUCCEEDED(initialPattern->get_CurrentVerticalScrollPercent(&restoredPercent)) && std::abs(restoredPercent - initialPercent) <= 0.5;
			};
			return restoreWithFallback([&]() {
				if (!restoreUIA()) {
					return false;
				}
				restore.restored = true;
				return true;
			}, [&]() { return restore.restore(); });
		}
		return restore.restore();
	};
	for (int i = 1; i <= targetActions && i < opt.maxFrames; ++i) {
		if (scrollPatternAtBottom(initialPattern.get())) {
			applyControlledObservation(loopState, ControlledObservation::UiaBottom);
			pageEndConfirmed = true;
			break;
		}
		double beforePercent = -1;
		double viewSize = -1;
		bool hasBeforeMetrics = readScrollMetrics(initialPattern.get(), beforePercent, viewSize);
		if (!sendWheelInput(opt.hwnd, -WHEEL_DELTA, opt.wheelX, opt.wheelY)) {
			result.errorCode = "SCROLL_INPUT_FAILED";
			result.message = "Could not send the controlled scroll input.";
			if (!restoreOrigin()) {
				result.errorCode = "SCROLL_RESTORE_FAILED";
				result.message = "Scroll input failed and the original position could not be restored.";
			}
			return result;
		}
		restore.record(-WHEEL_DELTA);
		ControlledStepResult step;
		try {
			step = captureControlledStep(frameCapturer, frames.back(), sequence, opt);
		} catch (std::exception const& err) {
			result.errorCode = "SCROLL_CAPTURE_FAILED";
			result.message = std::string("Could not observe the controlled scroll step: ") + err.what();
			if (!restoreOrigin()) {
				result.errorCode = "SCROLL_RESTORE_FAILED";
				result.message = "Scroll observation failed and the original position could not be restored.";
			}
			return result;
		}
		double afterPercent = -1;
		double afterViewSize = -1;
		bool hasAfterMetrics = readScrollMetrics(initialPattern.get(), afterPercent, afterViewSize);
		if (!step.moved && hasBeforeMetrics && hasAfterMetrics && afterPercent > beforePercent+0.001 && !step.frame.bitmap.empty()) {
			int estimate = controlledUiaMovementEstimate(frames.back().height, beforePercent, afterPercent, viewSize, opt.overlapPx);
			if (estimate > 0) {
				step.moved = true;
				step.movement = estimate;
			}
		}
		if (!step.moved) {
			if (!step.visuallyChanged) {
				restore.discardLast();
			} else {
				result.errorCode = "SCROLL_ALIGNMENT_AMBIGUOUS";
				result.message = "The page moved, but the selected region could not be aligned safely. Capture stopped before sending another scroll input.";
				if (!restoreOrigin()) {
					result.errorCode = "SCROLL_RESTORE_FAILED";
					result.message = "Scroll alignment failed and the original position could not be restored.";
				}
				return result;
			}
			if (controlledNoMotionEnded(loopState.consecutiveNoMotion + 1)) {
				applyControlledObservation(loopState, ControlledObservation::NoMotion);
				pageEndConfirmed = true;
				break;
			}
			applyControlledObservation(loopState, ControlledObservation::NoMotion);
			continue;
		}
		restore.forwardMovements.back() = step.movement;
		if (!controlledStepCanBeAccepted(step.moved, step.settled, !step.frame.bitmap.empty())) {
			applyControlledObservation(loopState, ControlledObservation::SettleTimeout);
			result.errorCode = "SCROLL_CAPTURE_FAILED";
			result.message = "The controlled scroll step did not produce a usable captured frame.";
			if (!restoreOrigin()) {
				result.errorCode = "SCROLL_RESTORE_FAILED";
				result.message = "The scroll step failed and the original position could not be restored.";
			}
			return result;
		}
		applyControlledObservation(loopState, ControlledObservation::MovedSettled);
		frames.push_back(storedScrollFrame(opt, step.frame.bitmap));
		frameStepCounts.push_back(1);
		acceptedMovements.push_back(step.movement);
		if (hasAfterMetrics && afterPercent >= 99.9) {
			pageEndConfirmed = true;
			break;
		}
	}

	std::vector<int> frameOffsets;
	Bitmap stitched;
	bool composed = restoreBeforeCompose(restoreOrigin, [&]() {
		std::vector<int> estimates = controlledOverlapEstimates(frames, acceptedMovements);
		int composeX = opt.rangeRequested && opt.cropW > 0 ? opt.cropX : -1;
		int composeWidth = opt.rangeRequested ? opt.cropW : 0;
		stitched = stitchVertical(frames, opt.overlapPx, estimates, &frameOffsets, false, false, composeX, composeWidth);
	});
	if (!composed) {
		result.errorCode = "SCROLL_RESTORE_FAILED";
		result.message = "Captured the requested range but could not restore the original scroll position.";
		return result;
	}
	if (opt.cropW > 0 && opt.cropH > 0) {
		int cropX = opt.rangeRequested ? 0 : opt.cropX;
		int cropH = opt.rangeRequested ? controlledSelectionCropHeight(stitched.height, frames.front().height, opt, pageEndConfirmed) : opt.cropH;
        if (cropH <= 0 || stitched.width < cropX + opt.cropW || stitched.height < opt.cropY + cropH) {
            result.errorCode = "RANGE_SCROLL_INCOMPLETE";
            result.message = "Requested scroll range was not fully captured.";
            return result;
        }
        stitched = cropBitmap(stitched, cropX, opt.cropY, opt.cropW, cropH);
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

	return captureScrollWithWheelFallback(opt, d3dDevice.get(), winrtDevice, item);
}

std::string hresultMessage(winrt::hresult_error const& err) {
    std::ostringstream out;
    out << narrow(std::wstring(err.message().c_str())) << " (HRESULT 0x" << std::hex << std::uppercase << static_cast<uint32_t>(err.code()) << ")";
    return out.str();
}

} // namespace

#ifndef INSTANTCAPTURE_HELPER_TEST
int main(int argc, char** argv) {
    CaptureResult result;
    try {
        winrt::init_apartment(winrt::apartment_type::multi_threaded);
        Options opt = parseArgs(argc, argv);
        result.outputPath = opt.outputPath;
        if (opt.command == "capture-scroll") {
            result = captureScroll(opt);
        } else if (opt.command == "query-scroll") {
            result = queryScrollPosition(opt);
        } else if (opt.command == "restore-scroll") {
            result = restoreScrollPosition(opt);
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
#endif
