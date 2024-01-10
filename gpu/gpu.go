//go:build linux || windows

package gpu

/*
#cgo linux LDFLAGS: -lrt -lpthread -ldl -lstdc++ -lm
#cgo windows LDFLAGS: -lpthread

#include "gpu_info.h"

*/
import "C"
import (
	"fmt"
	"log"
	"runtime"
	"sync"
	"unsafe"
)

type handles struct {
	cuda *C.cuda_handle_t
	rocm *C.rocm_handle_t
}

var gpuMutex sync.Mutex
var gpuHandles *handles = nil

// With our current CUDA compile flags, 5.2 and older will not work properly
const CudaComputeMajorMin = 6

// Note: gpuMutex must already be held
func initGPUHandles() {
	// TODO - if the ollama build is CPU only, don't do these checks as they're irrelevant and confusing
	log.Printf("Detecting GPU type")
	gpuHandles = &handles{nil, nil}
	var resp C.cuda_init_resp_t
	C.cuda_init(&resp)
	if resp.err != nil {
		log.Printf("CUDA not detected: %s", C.GoString(resp.err))
		C.free(unsafe.Pointer(resp.err))

		var resp C.rocm_init_resp_t
		C.rocm_init(&resp)
		if resp.err != nil {
			log.Printf("ROCm not detected: %s", C.GoString(resp.err))
			C.free(unsafe.Pointer(resp.err))
		} else {
			log.Printf("Radeon GPU detected")
			rocm := resp.rh
			gpuHandles.rocm = &rocm
		}
	} else {
		log.Printf("Nvidia GPU detected")
		cuda := resp.ch
		gpuHandles.cuda = &cuda
	}
}

func GetGPUInfo() GpuInfo {
	// TODO - consider exploring lspci (and equivalent on windows) to check for
	// GPUs so we can report warnings if we see Nvidia/AMD but fail to load the libraries
	gpuMutex.Lock()
	defer gpuMutex.Unlock()
	if gpuHandles == nil {
		initGPUHandles()
	}

	var memInfo C.mem_info_t
	resp := GpuInfo{}
	if gpuHandles.cuda != nil {
		C.cuda_check_vram(*gpuHandles.cuda, &memInfo)
		if memInfo.err != nil {
			log.Printf("error looking up CUDA GPU memory: %s", C.GoString(memInfo.err))
			C.free(unsafe.Pointer(memInfo.err))
		} else {
			// Verify minimum compute capability
			var cc C.cuda_compute_capability_t
			C.cuda_compute_capability(*gpuHandles.cuda, &cc)
			if cc.err != nil {
				log.Printf("error looking up CUDA GPU compute capability: %s", C.GoString(cc.err))
				C.free(unsafe.Pointer(cc.err))
			} else if cc.major >= CudaComputeMajorMin {
				log.Printf("CUDA Compute Capability detected: %d.%d", cc.major, cc.minor)
				resp.Library = "cuda"
			} else {
				log.Printf("CUDA GPU is too old. Falling back to CPU mode. Compute Capability detected: %d.%d", cc.major, cc.minor)
			}
		}
	} else if gpuHandles.rocm != nil {
		C.rocm_check_vram(*gpuHandles.rocm, &memInfo)
		if memInfo.err != nil {
			log.Printf("error looking up ROCm GPU memory: %s", C.GoString(memInfo.err))
			C.free(unsafe.Pointer(memInfo.err))
		} else {
			resp.Library = "rocm"
		}
	}
	if resp.Library == "" {
		C.cpu_check_ram(&memInfo)
		// In the future we may offer multiple CPU variants to tune CPU features
		if runtime.GOOS == "windows" {
			resp.Library = "cpu"
		} else {
			resp.Library = "default"
		}
	}
	if memInfo.err != nil {
		log.Printf("error looking up CPU memory: %s", C.GoString(memInfo.err))
		C.free(unsafe.Pointer(memInfo.err))
		return resp
	}

	resp.DeviceCount = uint32(memInfo.count)
	resp.FreeMemory = uint64(memInfo.free)
	resp.TotalMemory = uint64(memInfo.total)
	return resp
}

func getCPUMem() (memInfo, error) {
	var ret memInfo
	var info C.mem_info_t
	C.cpu_check_ram(&info)
	if info.err != nil {
		defer C.free(unsafe.Pointer(info.err))
		return ret, fmt.Errorf(C.GoString(info.err))
	}
	ret.FreeMemory = uint64(info.free)
	ret.TotalMemory = uint64(info.total)
	return ret, nil
}

func CheckVRAM() (int64, error) {
	gpuInfo := GetGPUInfo()
	if gpuInfo.FreeMemory > 0 && (gpuInfo.Library == "cuda" || gpuInfo.Library == "rocm") {
		// leave 10% or 512MiB of VRAM free per GPU to handle unaccounted for overhead
		gpus := uint64(gpuInfo.DeviceCount)
		overhead := gpuInfo.FreeMemory * gpus / 10
		if overhead < gpus*512*1024*1024 {
			overhead = gpus * 512 * 1024 * 1024
		}
		return int64(gpuInfo.FreeMemory - overhead), nil
	}

	return 0, fmt.Errorf("no GPU detected") // TODO - better handling of CPU based memory determiniation
}
