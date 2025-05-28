#include "string_operations.h"
#include <string.h>

int string_length(const char *str) {
    return strlen(str);
}

void reverse_string(char *str) {
    int length = string_length(str);
    for (int i = 0; i < length / 2; i++) {
        char temp = str[i];
        str[i] = str[length - i - 1];
        str[length - i - 1] = temp;
    }
}
