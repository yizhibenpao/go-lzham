#include <fstream>
#include <vector>
#include <string>
#include "lzham_core.h"
#define private public
#include "lzham_lzcomp_internal.h"
#undef private
#include "lzham_checksum.h"

using namespace lzham;
struct step { int pos, len, dist; };

int main(int argc, char** argv) {
    if (argc != 3) return 2;
    unsigned config = std::stoul(argv[1]);
    std::vector<unsigned char> src, output;
    std::vector<std::vector<step>> blocks;
    std::vector<unsigned> starts;
    uint32_t rng = 12345;
    for (int block = 0; block < 3; block++) {
        starts.push_back(src.size());
        std::vector<step> plan;
        int hist[4] = {1,1,1,1};
        auto literal = [&](unsigned char c) {
            plan.push_back({(int)src.size(), 0, 0});
            src.push_back(c);
        };
        auto match = [&](int n, int d) {
            int actual = d < 0 ? hist[-d-1] : d;
            plan.push_back({(int)src.size(), n, d});
            for (int i = 0; i < n; i++) src.push_back(src[src.size()-actual]);
            if (d < 0) {
                int rep = -d-1;
                for (int i = rep; i > 0; i--) hist[i] = hist[i-1];
            } else {
                for (int i = 3; i > 0; i--) hist[i] = hist[i-1];
            }
            hist[0] = actual;
        };
        for (int i = 0; i < 48192; i++) {
            rng ^= rng << 13; rng ^= rng >> 17; rng ^= rng << 5;
            literal(rng & (i < 8192 ? 255 : 7));
        }
        // All distance slots, short/large lengths, all rep indices and delta literals.
        for (int d : {1,2,3,4,7,8,15,16,31,32,63,64,127,128,255,256,511,512,1023,1024,2047,2048,4095,4096,8191}) {
            match(2, d);
            match(3, -1); match(4, -2); match(5, -3); match(6, -4);
            literal(37);
            match(1, -1);
        }
        for (int n : {9,256,257,258,513,514,1537,1538,5633,5634,65536}) {
            match(n, 1);
            match(n, -1);
        }
        plan.push_back({(int)src.size(), -1, 0});
        for (int i = 0; i < 128; i++) literal(i);
        blocks.push_back(plan);
    }
    CLZBase base;
    base.init_position_slots(20);
    base.init_slot_tabs();
    search_accelerator dict;
    if (!dict.init(&base, nullptr, 0, 1<<20, 1, false, 1)) return 3;
    if (!dict.add_bytes_begin(src.size(), src.data())) return 4;
    dict.add_bytes_end();
    lzcompressor::state state;
    if (!state.init(base, config&2, config&1)) return 5;
    for (int block = 0; block < 3; block++) {
        symbol_codec codec;
        codec.start_encoding(32768);
        if (!block) codec.encode_bits(config, 2);
        codec.encode_bits(CLZBase::cCompBlock, 2);
        codec.encode_arith_init();
        codec.encode_bits(block, 2);
        if (block == 1) state.reset_update_rate();
        if (block == 2) state.reset();
        state.start_of_block(dict, starts[block], block);
        for (auto d : blocks[block]) {
            if (d.len == -1) {
                if (!state.encode_reset_state_partial(codec, dict, d.pos)) return 6;
            } else if (!state.encode(codec, base, dict, lzcompressor::lzdecision(d.pos, d.len, d.dist))) return 7;
        }
        unsigned end = block == 2 ? src.size() : starts[block+1];
        if (!state.encode_eob(codec, dict, end)) return 8;
        codec.stop_encoding(true);
        auto &buf = codec.get_encoding_buf();
        output.insert(output.end(), buf.get_ptr(), buf.get_ptr()+buf.size());
    }
    // Build an all-X history, then force both ends of every distance slot.
    // The state encoder only reads bytes from the accelerator for these calls.
    memset(dict.m_dict.get_ptr(), 'X', dict.m_dict.size());
    for (int part = 0; part < 2; part++) {
        symbol_codec codec;
        codec.start_encoding(4096);
        codec.encode_bits(CLZBase::cCompBlock, 2);
        codec.encode_arith_init();
        codec.encode_bits(2, 2);
        state.reset();
        state.start_of_block(dict, 0, 3+part);
        unsigned pos = 0;
        auto emit = [&](int n, int d) {
            if (!state.encode(codec, base, dict, lzcompressor::lzdecision(pos, n, d))) exit(9);
            unsigned count = n ? n : 1;
            pos += count;
            src.insert(src.end(), count, 'X');
        };
        if (!part) {
            emit(0, 0);
            for (int i = 0; i < 15; i++) emit(65536, -1);
            emit(65535, -1);
        } else {
            for (unsigned slot = 1; slot < base.m_num_lzx_slots; slot++) {
                unsigned lo = base.m_lzx_position_base[slot];
                unsigned hi = lo + (1U<<base.m_lzx_position_extra_bits[slot])-1;
                emit(2, lo);
                emit(9, hi);
                emit(257, lo);
            }
        }
        if (!state.encode_eob(codec, dict, pos)) return 10;
        codec.stop_encoding(true);
        auto &buf = codec.get_encoding_buf();
        output.insert(output.end(), buf.get_ptr(), buf.get_ptr()+buf.size());
    }
    symbol_codec tail;
    tail.start_encoding(16);
    tail.encode_bits(CLZBase::cEOFBlock, 2);
    tail.encode_align_to_byte();
    tail.encode_bits(adler32(src.data(), src.size()), 32);
    tail.encode_bits(crc32(0, src.data(), src.size()), 32);
    tail.stop_encoding(false);
    auto &buf = tail.get_encoding_buf();
    output.insert(output.end(), buf.get_ptr(), buf.get_ptr()+buf.size());
    std::ofstream cmp(std::string(argv[2])+".lzham", std::ios::binary);
    cmp.write((const char*)output.data(), output.size());
    std::ofstream raw(std::string(argv[2])+".raw", std::ios::binary);
    raw.write((const char*)src.data(), src.size());
}
