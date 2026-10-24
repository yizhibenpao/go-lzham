#include "tf2lzham.h"
#include "lzham.h"
#include <fstream>
#include <iostream>
#include <vector>
#include <cstdint>

int main(int argc, char** argv) {
    if (argc < 4) return 2;
    std::ifstream input(argv[2], std::ios::binary);
    std::vector<uint8_t> src((std::istreambuf_iterator<char>(input)), {});
    std::vector<uint8_t> dst(src.size() * 2 + 1024);
    size_t dst_len = dst.size();
    uint32_t adler = 0, crc = 0, status = 0;
    if (argv[1][0] == 'c') {
        if (argc > 4) {
            lzham_compress_params params = {};
            params.m_struct_size = sizeof(params);
            params.m_dict_size_log2 = 20;
            params.m_level = argc > 5 ? (lzham_compress_level)std::stoul(argv[5]) : LZHAM_COMP_LEVEL_UBER;
            params.m_compress_flags = std::stoul(argv[4]);
            status = lzham_compress_memory(&params, dst.data(), &dst_len, src.data(), src.size(), &adler, &crc);
        } else {
            status = tf2lzham_compress(dst.data(), &dst_len, src.data(), src.size(), &adler, &crc);
        }
    } else {
        dst.assign(argc > 4 ? std::stoul(argv[4]) : 64 * 1024 * 1024, 0);
        dst_len = dst.size();
        status = tf2lzham_decompress(dst.data(), &dst_len, src.data(), src.size(), &adler, &crc);
    }
    if (status != 3) {
        const char* message = argv[1][0] == 'c' ? tf2lzham_compress_strerror(status) : tf2lzham_decompress_strerror(status);
        std::cerr << "status=" << status << " error=" << message << "\n";
        return 1;
    }
    std::ofstream output(argv[3], std::ios::binary);
    output.write(reinterpret_cast<const char*>(dst.data()), dst_len);
    std::cout << "n=" << dst_len << " adler=" << std::hex << adler << " crc=" << crc << "\n";
    return 0;
}
