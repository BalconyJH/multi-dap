/* Compile against the operator-selected installed headers. This probe reports
 * host ABI layout only; it links no vendor library and contacts no debugger. */
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>

#include "timemachine_api.h"
#include "ts_packet.h"

int main(void) {
    uint16_t endian_check = 1;
    const char *byte_order = *((unsigned char *)&endian_check) ? "little" : "big";
    printf("{\"schema\":\"multi-dap.timemachine-abi-probe.v1\",");
    printf("\"pointer_bits\":%u,\"byte_order\":\"%s\",",
           (unsigned)(sizeof(void *) * 8), byte_order);
    printf("\"info_size\":%u,\"info_timebase_offset\":%u,\"info_identifier_offset\":%u,",
           (unsigned)sizeof(TM_DATA_INFO),
           (unsigned)offsetof(TM_DATA_INFO, timebase),
           (unsigned)offsetof(TM_DATA_INFO, identifier));
    printf("\"packet_size\":%u,\"packet_alignment\":%u,",
           (unsigned)sizeof(TS_Packet), (unsigned)_Alignof(TS_Packet));
    printf("\"type_offset\":%u,\"reconstructed_offset\":%u,\"address_offset\":%u,\"payload_offset\":%u,",
           (unsigned)offsetof(TS_Packet, type),
           (unsigned)offsetof(TS_Packet, reconstructed),
           (unsigned)offsetof(TS_Packet, address),
           (unsigned)offsetof(TS_Packet, u));
    printf("\"function_event_type_offset\":%u,\"function_event_timetag_offset\":%u",
           (unsigned)offsetof(TS_Packet_Union, fn_event.type),
           (unsigned)offsetof(TS_Packet_Union, fn_event.timetag));
#ifndef MULTI_API_LEGACY
    {
        TS_Packet_WallClockSync sample;
        uint64_t packed = 0;
        memset(&sample, 0, sizeof(sample));
        sample.seconds = UINT64_C(0x0102030405);
        sample.microseconds = UINT64_C(0x050607);
        memcpy(&packed, ((unsigned char *)&sample) + 8, sizeof(packed));
        printf(",\"wall_clock_size\":%u,\"wall_clock_seconds\":\"%llu\","
               "\"wall_clock_microseconds\":\"%llu\",\"wall_clock_packed\":\"%llu\"",
               (unsigned)sizeof(sample),
               (unsigned long long)sample.seconds,
               (unsigned long long)sample.microseconds,
               (unsigned long long)packed);
    }
#endif
    puts("}");
    return 0;
}
