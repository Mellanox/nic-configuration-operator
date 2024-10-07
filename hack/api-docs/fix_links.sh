#!/bin/bash
# This script ad-hoc fixes references in api doc

DOC=$(realpath $1)

awk '
{
    gsub(/\(#configuration.net.nvidia.com%2fv1alpha1)/, "(#configurationnetnvidiacomv1alpha1)");
    gsub(/#configuration.net.nvidia.com\/v1alpha1./, "#");
}1
' ${DOC} > ${DOC}.tmp
mv -f ${DOC}.tmp ${DOC}
