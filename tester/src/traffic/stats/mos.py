# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# MOS estimation — ITU-T G.107 E-model

import math


def estimate_mos(delay_ms: float, jitter_ms: float, loss_pct: float) -> float:
    """Estimate MOS from simplified E-model (ITU-T G.107).

    R = 93.2 - Id - Ie
    MOS = 1 + 0.035*R + R*(R-60)*(100-R)*7e-6

    Optimized for AMR-WB codec with jitter buffer.
    """
    d = delay_ms + 2 * jitter_ms
    Id = 0.024 * d
    if d > 177.3:
        Id += 0.11 * (d - 177.3)

    e = loss_pct / 100.0
    Ie = 7 + 30 * math.log(1 + 15 * e) if e > 0 else 7

    R = max(0, min(100, 93.2 - Id - Ie))

    if R < 6.5:
        mos = 1.0
    elif R > 100:
        mos = 4.5
    else:
        mos = 1 + 0.035 * R + R * (R - 60) * (100 - R) * 7e-6

    return max(1.0, min(5.0, mos))
