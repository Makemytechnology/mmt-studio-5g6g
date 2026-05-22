/* Copyright (c) 2026 MakeMyTechnology. All rights reserved. */
/* upf_sdf_parser.h — SDF filter string parser
 *
 * Parses IPFilterRule strings per IETF RFC 6733 / 3GPP TS 29.244:
 *   "permit out ip from 10.0.0.0/8 to any"
 *   "permit out 17 from any to 10.45.0.2 1234"
 *
 * Format: action direction proto from src [port] to dst [port]
 */

#ifndef UPF_SDF_PARSER_H
#define UPF_SDF_PARSER_H

#include "upf_types.h"

/* Parse an SDF filter rule string into the binary struct.
 * Returns 0 on success, -1 on parse error. */
int upf_sdf_parse(const char *rule_str, upf_sdf_filter_t *out);

/* Format an SDF filter back to string (for logging).
 * Returns number of chars written (excluding null), or -1 on error. */
int upf_sdf_format(const upf_sdf_filter_t *filter, char *buf, size_t bufsz);

#endif /* UPF_SDF_PARSER_H */
