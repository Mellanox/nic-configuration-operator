The DMSPlanParams CRD and minimal compiler/YANG fixtures originate from the
published dms-lib revision 31212c4d0b9a. The blueprint adds a second rail and a per-device step barrier to test
that full-host compilation does not authorize unselected ASIC writes or confuse
step ordering with host-wide ownership. These
fixtures exercise the real compiler and API server; they are not hardware or
production DOCA schema qualification.
