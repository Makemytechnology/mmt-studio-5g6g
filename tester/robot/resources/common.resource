# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
*** Settings ***
Documentation    Common resources for SA Core 5G test suites
Library          ../libraries/GnbLibrary.py
Library          ../libraries/UeLibrary.py
Library          ../libraries/ProtocolLibrary.py
Library          Collections
Library          String

*** Variables ***
${GNB_NAME}          tester-gnb-00
${DEFAULT_TIMEOUT}   15
# Traffic/media duration in seconds — set to 60 during debug, reduce to 10 for automation
${TRAFFIC_DURATION}  60
${UE_1}         001011234560001
${UE_2}         001011234560002
${UE_3}         001011234560003
${UE_4}         001011234560004
${UE_5}         001011234560005
${UE_6}         001011234560006
${UE_7}         001011234560007
${UE_8}         001011234560008
${UE_9}         001011234560009
${UE_10}        001011234560010
${UE_11}        001011234560011
${UE_12}        001011234560012
${UE_13}        001011234560013
${UE_14}        001011234560014
${UE_15}        001011234560015
${UE_16}        001011234560016
${UE_17}        001011234560017
${UE_18}        001011234560018
${UE_19}        001011234560019
${UE_20}        001011234560020
${UE_21}        001011234560021
${UE_22}        001011234560022
${UE_23}        001011234560023
${UE_24}        001011234560024
${UE_25}        001011234560025
${UE_26}        001011234560026
${UE_27}        001011234560027
${UE_28}        001011234560028
${UE_29}        001011234560029
${UE_30}        001011234560030
${UE_31}        001011234560031
${UE_32}        001011234560032
${UE_33}        001011234560033
${UE_34}        001011234560034
${UE_35}        001011234560035
${UE_36}        001011234560036
${UE_37}        001011234560037
${UE_38}        001011234560038
${UE_39}        001011234560039
${UE_40}        001011234560040
${UE_41}        001011234560041
${UE_42}        001011234560042
${UE_43}        001011234560043
${UE_44}        001011234560044
${UE_45}        001011234560045
${UE_46}        001011234560046
${UE_47}        001011234560047
${UE_48}        001011234560048
${UE_49}        001011234560049
${UE_50}        001011234560050
${UE_51}        001011234560051
${UE_52}        001011234560052
${UE_53}        001011234560053
${UE_54}        001011234560054
${UE_55}        001011234560055
${UE_56}        001011234560056
${UE_57}        001011234560057
${UE_58}        001011234560058
${UE_59}        001011234560059
${UE_60}        001011234560060
${UE_61}        001011234560061
${UE_62}        001011234560062
${UE_63}        001011234560063
${UE_64}        001011234560064
${UE_65}        001011234560065
${UE_66}        001011234560066
${UE_67}        001011234560067
${UE_68}        001011234560068
${UE_69}        001011234560069
${UE_70}        001011234560070
${UE_71}        001011234560071
${UE_72}        001011234560072
${UE_73}        001011234560073
${UE_74}        001011234560074
${UE_75}        001011234560075
${UE_76}        001011234560076
${UE_77}        001011234560077
${UE_78}        001011234560078
${UE_79}        001011234560079
${UE_80}        001011234560080
${UE_81}        001011234560081
${UE_82}        001011234560082
${UE_83}        001011234560083
${UE_84}        001011234560084
${UE_85}        001011234560085
${UE_86}        001011234560086
${UE_87}        001011234560087
${UE_88}        001011234560088
${UE_89}        001011234560089
${UE_90}        001011234560090
${UE_91}        001011234560091
${UE_92}        001011234560092
${UE_93}        001011234560093
${UE_94}        001011234560094
${UE_95}        001011234560095
${UE_96}        001011234560096
${UE_97}        001011234560097
${UE_98}        001011234560098
${UE_99}        001011234560099
${UE_100}       001011234560100
${UE_101}       001011234560101
${UE_102}       001011234560102
${UE_103}       001011234560103
${UE_104}       001011234560104
${UE_105}       001011234560105
${UE_106}       001011234560106
${UE_107}       001011234560107
${UE_108}       001011234560108
${UE_109}       001011234560109
${UE_110}       001011234560110
${UE_111}       001011234560111
${UE_112}       001011234560112
${UE_113}       001011234560113
${UE_114}       001011234560114
${UE_115}       001011234560115
${UE_116}       001011234560116
${UE_117}       001011234560117
${UE_118}       001011234560118
${UE_119}       001011234560119
${UE_120}       001011234560120
${UE_121}       001011234560121
${UE_122}       001011234560122
${UE_123}       001011234560123
${UE_124}       001011234560124
${UE_125}       001011234560125
${UE_126}       001011234560126
${UE_127}       001011234560127
${UE_128}       001011234560128

*** Keywords ***
Setup Test Environment
    [Documentation]    Create gNB from config profile + load UEs from UE config DB. Use in Suite Setup.
    ${gnb}=    Create gNB From Config    ${GNB_NAME}
    Connect gNB And Wait Ready    ${gnb}
    Set Suite Variable    ${GNB}    ${gnb}
    Load UEs From Config
    Log    Test environment ready: gNB=${GNB_NAME}

Teardown Test Environment
    [Documentation]    Cleanup. Use in Suite Teardown.
    Remove All UEs
    Remove All gNBs

Full Registration
    [Documentation]    Register a UE end-to-end (attach + register + wait).
    [Arguments]    ${imsi}    ${gnb}=${GNB}    ${timeout}=${DEFAULT_TIMEOUT}
    Register UE And Wait    ${imsi}    ${gnb}    timeout=${timeout}
    UE Should Be Registered    ${imsi}
    UE Should Have Security Keys    ${imsi}

Full PDU Session
    [Documentation]    Establish PDU session after registration.
    [Arguments]    ${imsi}    ${dnn}=internet    ${psi}=1    ${timeout}=${DEFAULT_TIMEOUT}
    ${ip}=    Establish PDU Session And Wait    ${imsi}    dnn=${dnn}    psi=${psi}    timeout=${timeout}
    RETURN    ${ip}

Full Registration And PDU Session
    [Documentation]    Register + establish PDU session.
    [Arguments]    ${imsi}    ${gnb}=${GNB}    ${dnn}=internet    ${psi}=1
    Full Registration    ${imsi}    ${gnb}
    ${ip}=    Full PDU Session    ${imsi}    ${dnn}    ${psi}
    RETURN    ${ip}
