#include <stdio.h>
#include "math_operations.h"
#include "string_operations.h"

int main() {
    // 测试数学运算
    printf("Math Operations:\n");
    printf("10 + 5 = %d\n", add(10, 5));
    printf("10 - 5 = %d\n", subtract(10, 5));
    printf("10 * 5 = %d\n", multiply(10, 5));

    // 测试字符串操作
    char str[] = "Hello Android";
    printf("\nString Operations:\n");
    printf("Original: %s\n", str);
    printf("Length: %d\n", string_length(str));
    
    reverse_string(str);
    printf("Reversed: %s\n", str);
    
    return 0;
}
