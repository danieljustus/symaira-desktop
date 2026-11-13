//! Ports of Go 1.26's `sort.Slice` (pattern-defeating quicksort, not stable)
//! and `sort.SliceStable`, plus `container/heap`'s sift routines.
//!
//! Go's retrieval code ranks with these exact algorithms. Their results
//! differ from any other sort whenever the comparator reports ties, so a
//! faithful port is the only way to reproduce Go's order for equal scores.

/// Mirrors Go's internal `lessSwap`: `less(i, j)` compares elements at the
/// given indexes of the slice being sorted.
struct Sorter<'a, T, F: Fn(&T, &T) -> bool> {
    data: &'a mut [T],
    less: F,
}

impl<T, F: Fn(&T, &T) -> bool> Sorter<'_, T, F> {
    fn less(&self, i: usize, j: usize) -> bool {
        (self.less)(&self.data[i], &self.data[j])
    }

    fn swap(&mut self, i: usize, j: usize) {
        self.data.swap(i, j);
    }
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum SortedHint {
    Unknown,
    Increasing,
    Decreasing,
}

/// Go's `sort.Slice(x, less)`.
pub(crate) fn slice_sort_by<T>(data: &mut [T], less: impl Fn(&T, &T) -> bool) {
    let length = data.len();
    let limit = (usize::BITS - length.leading_zeros()) as usize;
    let mut sorter = Sorter { data, less };
    pdqsort(&mut sorter, 0, length, limit);
}

/// Go's `sort.SliceStable(x, less)`.
pub(crate) fn slice_stable_sort_by<T>(data: &mut [T], less: impl Fn(&T, &T) -> bool) {
    let length = data.len();
    let mut sorter = Sorter { data, less };
    stable(&mut sorter, length);
}

fn insertion_sort<T, F: Fn(&T, &T) -> bool>(data: &mut Sorter<'_, T, F>, a: usize, b: usize) {
    for i in (a + 1)..b {
        let mut j = i;
        while j > a && data.less(j, j - 1) {
            data.swap(j, j - 1);
            j -= 1;
        }
    }
}

fn sift_down<T, F: Fn(&T, &T) -> bool>(
    data: &mut Sorter<'_, T, F>,
    lo: usize,
    hi: usize,
    first: usize,
) {
    let mut root = lo;
    loop {
        let mut child = 2 * root + 1;
        if child >= hi {
            break;
        }
        if child + 1 < hi && data.less(first + child, first + child + 1) {
            child += 1;
        }
        if !data.less(first + root, first + child) {
            return;
        }
        data.swap(first + root, first + child);
        root = child;
    }
}

fn heap_sort<T, F: Fn(&T, &T) -> bool>(data: &mut Sorter<'_, T, F>, a: usize, b: usize) {
    let first = a;
    let hi = b - a;
    let mut i = (hi as isize - 1) / 2;
    while i >= 0 {
        sift_down(data, i as usize, hi, first);
        i -= 1;
    }
    let mut i = hi as isize - 1;
    while i >= 0 {
        data.swap(first, first + i as usize);
        sift_down(data, 0, i as usize, first);
        i -= 1;
    }
}

fn pdqsort<T, F: Fn(&T, &T) -> bool>(
    data: &mut Sorter<'_, T, F>,
    mut a: usize,
    mut b: usize,
    mut limit: usize,
) {
    const MAX_INSERTION: usize = 12;
    let mut was_balanced = true;
    let mut was_partitioned = true;
    loop {
        let length = b - a;
        if length <= MAX_INSERTION {
            insertion_sort(data, a, b);
            return;
        }
        if limit == 0 {
            heap_sort(data, a, b);
            return;
        }
        if !was_balanced {
            break_patterns(data, a, b);
            limit -= 1;
        }
        let (mut pivot, mut hint) = choose_pivot(data, a, b);
        if hint == SortedHint::Decreasing {
            reverse_range(data, a, b);
            pivot = (b - 1) - (pivot - a);
            hint = SortedHint::Increasing;
        }
        if was_balanced
            && was_partitioned
            && hint == SortedHint::Increasing
            && partial_insertion_sort(data, a, b)
        {
            return;
        }
        if a > 0 && !data.less(a - 1, pivot) {
            let mid = partition_equal(data, a, b, pivot);
            a = mid;
            continue;
        }
        let (mid, already_partitioned) = partition(data, a, b, pivot);
        was_partitioned = already_partitioned;
        let (left_len, right_len) = (mid - a, b - mid);
        let balance_threshold = length / 8;
        if left_len < right_len {
            was_balanced = left_len >= balance_threshold;
            pdqsort(data, a, mid, limit);
            a = mid + 1;
        } else {
            was_balanced = right_len >= balance_threshold;
            pdqsort(data, mid + 1, b, limit);
            b = mid;
        }
    }
}

fn partition<T, F: Fn(&T, &T) -> bool>(
    data: &mut Sorter<'_, T, F>,
    a: usize,
    b: usize,
    pivot: usize,
) -> (usize, bool) {
    data.swap(a, pivot);
    // Indexes are inclusive; `j` can reach `a`, so keep them signed.
    let (mut i, mut j) = (a as isize + 1, b as isize - 1);
    while i <= j && data.less(i as usize, a) {
        i += 1;
    }
    while i <= j && !data.less(j as usize, a) {
        j -= 1;
    }
    if i > j {
        data.swap(j as usize, a);
        return (j as usize, true);
    }
    data.swap(i as usize, j as usize);
    i += 1;
    j -= 1;
    loop {
        while i <= j && data.less(i as usize, a) {
            i += 1;
        }
        while i <= j && !data.less(j as usize, a) {
            j -= 1;
        }
        if i > j {
            break;
        }
        data.swap(i as usize, j as usize);
        i += 1;
        j -= 1;
    }
    data.swap(j as usize, a);
    (j as usize, false)
}

fn partition_equal<T, F: Fn(&T, &T) -> bool>(
    data: &mut Sorter<'_, T, F>,
    a: usize,
    b: usize,
    pivot: usize,
) -> usize {
    data.swap(a, pivot);
    let (mut i, mut j) = (a as isize + 1, b as isize - 1);
    loop {
        while i <= j && !data.less(a, i as usize) {
            i += 1;
        }
        while i <= j && data.less(a, j as usize) {
            j -= 1;
        }
        if i > j {
            break;
        }
        data.swap(i as usize, j as usize);
        i += 1;
        j -= 1;
    }
    i as usize
}

fn partial_insertion_sort<T, F: Fn(&T, &T) -> bool>(
    data: &mut Sorter<'_, T, F>,
    a: usize,
    b: usize,
) -> bool {
    const MAX_STEPS: usize = 5;
    const SHORTEST_SHIFTING: usize = 50;
    let mut i = a + 1;
    for _ in 0..MAX_STEPS {
        while i < b && !data.less(i, i - 1) {
            i += 1;
        }
        if i == b {
            return true;
        }
        if b - a < SHORTEST_SHIFTING {
            return false;
        }
        data.swap(i, i - 1);
        if i - a >= 2 {
            let mut j = i - 1;
            while j >= 1 {
                if !data.less(j, j - 1) {
                    break;
                }
                data.swap(j, j - 1);
                j -= 1;
            }
        }
        if b - i >= 2 {
            let mut j = i + 1;
            while j < b {
                if !data.less(j, j - 1) {
                    break;
                }
                data.swap(j, j - 1);
                j += 1;
            }
        }
    }
    false
}

fn break_patterns<T, F: Fn(&T, &T) -> bool>(data: &mut Sorter<'_, T, F>, a: usize, b: usize) {
    let length = b - a;
    if length >= 8 {
        let mut random = length as u64;
        let modulus = 1_usize << (usize::BITS - length.leading_zeros());
        let start = a + (length / 4) * 2 - 1;
        for idx in start..=(a + (length / 4) * 2 + 1) {
            random ^= random << 13;
            random ^= random >> 7;
            random ^= random << 17;
            let mut other = (random as usize) & (modulus - 1);
            if other >= length {
                other -= length;
            }
            data.swap(idx, a + other);
        }
    }
}

fn choose_pivot<T, F: Fn(&T, &T) -> bool>(
    data: &mut Sorter<'_, T, F>,
    a: usize,
    b: usize,
) -> (usize, SortedHint) {
    const SHORTEST_NINTHER: usize = 50;
    const MAX_SWAPS: usize = 4 * 3;
    let l = b - a;
    let mut swaps = 0;
    let mut i = a + l / 4;
    let mut j = a + l / 4 * 2;
    let mut k = a + l / 4 * 3;
    if l >= 8 {
        if l >= SHORTEST_NINTHER {
            i = median(data, i - 1, i, i + 1, &mut swaps);
            j = median(data, j - 1, j, j + 1, &mut swaps);
            k = median(data, k - 1, k, k + 1, &mut swaps);
        }
        j = median(data, i, j, k, &mut swaps);
    }
    match swaps {
        0 => (j, SortedHint::Increasing),
        MAX_SWAPS => (j, SortedHint::Decreasing),
        _ => (j, SortedHint::Unknown),
    }
}

fn order2<T, F: Fn(&T, &T) -> bool>(
    data: &Sorter<'_, T, F>,
    a: usize,
    b: usize,
    swaps: &mut usize,
) -> (usize, usize) {
    if data.less(b, a) {
        *swaps += 1;
        return (b, a);
    }
    (a, b)
}

fn median<T, F: Fn(&T, &T) -> bool>(
    data: &Sorter<'_, T, F>,
    a: usize,
    b: usize,
    c: usize,
    swaps: &mut usize,
) -> usize {
    let (a, b) = order2(data, a, b, swaps);
    let (b, _c) = order2(data, b, c, swaps);
    let (_a, b) = order2(data, a, b, swaps);
    b
}

fn reverse_range<T, F: Fn(&T, &T) -> bool>(data: &mut Sorter<'_, T, F>, a: usize, b: usize) {
    let (mut i, mut j) = (a, b - 1);
    while i < j {
        data.swap(i, j);
        i += 1;
        j -= 1;
    }
}

fn swap_range<T, F: Fn(&T, &T) -> bool>(data: &mut Sorter<'_, T, F>, a: usize, b: usize, n: usize) {
    for i in 0..n {
        data.swap(a + i, b + i);
    }
}

fn stable<T, F: Fn(&T, &T) -> bool>(data: &mut Sorter<'_, T, F>, n: usize) {
    let mut block_size = 20;
    let (mut a, mut b) = (0, block_size);
    while b <= n {
        insertion_sort(data, a, b);
        a = b;
        b += block_size;
    }
    insertion_sort(data, a, n);
    while block_size < n {
        a = 0;
        b = 2 * block_size;
        while b <= n {
            sym_merge(data, a, a + block_size, b);
            a = b;
            b += 2 * block_size;
        }
        let m = a + block_size;
        if m < n {
            sym_merge(data, a, m, n);
        }
        block_size *= 2;
    }
}

fn sym_merge<T, F: Fn(&T, &T) -> bool>(data: &mut Sorter<'_, T, F>, a: usize, m: usize, b: usize) {
    if m - a == 1 {
        let (mut i, mut j) = (m, b);
        while i < j {
            let h = (i + j) >> 1;
            if data.less(h, a) {
                i = h + 1;
            } else {
                j = h;
            }
        }
        let mut k = a;
        while k + 1 < i {
            data.swap(k, k + 1);
            k += 1;
        }
        return;
    }
    if b - m == 1 {
        let (mut i, mut j) = (a, m);
        while i < j {
            let h = (i + j) >> 1;
            if !data.less(m, h) {
                i = h + 1;
            } else {
                j = h;
            }
        }
        let mut k = m;
        while k > i {
            data.swap(k, k - 1);
            k -= 1;
        }
        return;
    }
    let mid = (a + b) >> 1;
    let n = mid + m;
    let (mut start, mut r) = if m > mid { (n - b, mid) } else { (a, m) };
    let p = n - 1;
    while start < r {
        let c = (start + r) >> 1;
        if !data.less(p - c, c) {
            start = c + 1;
        } else {
            r = c;
        }
    }
    let end = n - start;
    if start < m && m < end {
        rotate(data, start, m, end);
    }
    if a < start && start < mid {
        sym_merge(data, a, start, mid);
    }
    if mid < end && end < b {
        sym_merge(data, mid, end, b);
    }
}

fn rotate<T, F: Fn(&T, &T) -> bool>(data: &mut Sorter<'_, T, F>, a: usize, m: usize, b: usize) {
    let mut i = m - a;
    let mut j = b - m;
    while i != j {
        if i > j {
            swap_range(data, m - i, m, j);
            i -= j;
        } else {
            swap_range(data, m - i, m + j - i, i);
            j -= i;
        }
    }
    swap_range(data, m - i, m, i);
}

/// `container/heap.up` for a min-heap ordered by `less`.
pub(crate) fn heap_up<T>(data: &mut [T], mut j: usize, less: &impl Fn(&T, &T) -> bool) {
    while j > 0 {
        let i = (j - 1) / 2;
        if !less(&data[j], &data[i]) {
            break;
        }
        data.swap(i, j);
        j = i;
    }
}

/// `container/heap.down`; reports whether the element moved.
pub(crate) fn heap_down<T>(
    data: &mut [T],
    i0: usize,
    n: usize,
    less: &impl Fn(&T, &T) -> bool,
) -> bool {
    let mut i = i0;
    loop {
        let j1 = 2 * i + 1;
        if j1 >= n {
            break;
        }
        let mut j = j1;
        let j2 = j1 + 1;
        if j2 < n && less(&data[j2], &data[j1]) {
            j = j2;
        }
        if !less(&data[j], &data[i]) {
            break;
        }
        data.swap(i, j);
        i = j;
    }
    i > i0
}

/// `container/heap.Fix`.
pub(crate) fn heap_fix<T>(data: &mut [T], i: usize, less: &impl Fn(&T, &T) -> bool) {
    let n = data.len();
    if !heap_down(data, i, n, less) {
        heap_up(data, i, less);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn stable_sort_keeps_equal_keys_in_input_order() {
        let mut values: Vec<(u32, usize)> = (0..200).map(|i| ((i % 7) as u32, i)).collect();
        slice_stable_sort_by(&mut values, |left, right| left.0 > right.0);
        assert!(values.windows(2).all(|pair| {
            pair[0].0 > pair[1].0 || (pair[0].0 == pair[1].0 && pair[0].1 < pair[1].1)
        }));
    }

    #[test]
    fn unstable_sort_orders_distinct_keys() {
        let mut values: Vec<u32> = (0..500).map(|i| (i * 7919) % 503).collect();
        slice_sort_by(&mut values, |left, right| left > right);
        assert!(values.windows(2).all(|pair| pair[0] >= pair[1]));
    }
}
