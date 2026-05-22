# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
#
"""Encode an NGSetupRequest using pycrate and print the APER bytes.

Compare against the asn1go output from TestRealNGSetupRequestRoundTrip.
"""
import sys
sys.path.insert(0, r"C:\Work-oldlaptop\marketing\edgeq\mmt-products\Eagle\Software\mmt_studio_core\libs\pycrate-master")

from pycrate_asn1dir import NGAP


def main():
    # NGSetupRequest is a top-level message; its IEs are the same shape we
    # generate in Go: list of {id, criticality, value, [presence]} entries.
    msg = NGAP.NGAP_PDU_Contents.NGSetupRequest

    val = {
        "protocolIEs": [
            {
                "id": 82,                           # id-RANNodeName
                "criticality": "ignore",
                "value": ("RANNodeName", "gNB-12345"),
            },
            {
                "id": 21,                           # id-DefaultPagingDRX
                "criticality": "ignore",
                "value": ("PagingDRX", "v128"),
            },
        ]
    }

    msg.set_val(val)
    aper_bytes = msg.to_aper()
    print(f"pycrate APER ({len(aper_bytes)} bytes):", aper_bytes.hex())
    uper_bytes = msg.to_uper()
    print(f"pycrate UPER ({len(uper_bytes)} bytes):", uper_bytes.hex())


if __name__ == "__main__":
    main()
