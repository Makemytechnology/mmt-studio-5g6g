

def decode_plmn_id(plmn_id: bytes) -> tuple:
    """
    Decode a 3-byte PLMN ID into MCC and MNC as per 3GPP TS 24.301.

    :param plmn_id: A 3-byte encoded PLMN ID
    :return: A tuple (MCC, MNC) as strings
    """
    if len(plmn_id) != 3:
        raise ValueError("PLMN ID must be exactly 3 bytes")

    # Extract MCC digits
    mcc = f"{plmn_id[0] & 0x0F}{(plmn_id[0] >> 4) & 0x0F}{plmn_id[1] & 0x0F}"

    # Extract MNC digits
    if (plmn_id[1] >> 4) == 0xF:
        # 2-digit MNC case (filler in the 3rd nibble)
        mnc = f"{plmn_id[2] & 0x0F}{(plmn_id[2] >> 4) & 0x0F}"
    else:
        # 3-digit MNC case
        mnc = f"{plmn_id[2] & 0x0F}{(plmn_id[2] >> 4) & 0x0F}{(plmn_id[1] >> 4) & 0x0F}"

    return mcc, mnc
    

def encode_plmn_id(mcc: str, mnc: str) -> bytes:
    """
    Convert MCC and MNC to a PLMN ID (3-byte format) as per 3GPP TS 24.301.
    
    :param mcc: Mobile Country Code (3 digits)
    :param mnc: Mobile Network Code (2 or 3 digits)
    :return: PLMN ID as a 3-byte value
    """
    if len(mcc) != 3 or len(mnc) not in (2, 3):
        raise ValueError("MCC must be 3 digits and MNC must be 2 or 3 digits")

    # Convert MCC and MNC into individual digits
    mcc_digits = [int(d) for d in mcc]
    mnc_digits = [int(d) for d in mnc]

    if len(mnc) == 2:
        # 2-digit MNC: Use Filler `0xF` in 3rd nibble
        plmn_id = bytes([
            (mcc_digits[1] << 4) | mcc_digits[0],  # MCC digit 2 & 1
            (0xF0 | mcc_digits[2]),  # MCC digit 3 & filler
            (mnc_digits[1] << 4) | mnc_digits[0]  # MNC digit 2 & 1
        ])
    else:
        # 3-digit MNC
        plmn_id = bytes([
            (mcc_digits[1] << 4) | mcc_digits[0],  # MCC digit 2 & 1
            (mnc_digits[2] << 4) | mcc_digits[2],  # MNC digit 3 & MCC digit 3
            (mnc_digits[1] << 4) | mnc_digits[0]  # MNC digit 2 & 1
        ])

    return plmn_id


