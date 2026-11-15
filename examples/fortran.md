# Fortran cells

The Fortran cells of a notebook share one session: what a cell defines (a module,
a derived type, a subroutine, a variable and the value it has) is there in the
cells after it. Their variables stay in Fortran; the Go cells do not see them.

A module with a function, in its own cell:

```fortran
module quadrature
  implicit none
contains
  function simpson(a, b, n) result(s)
    real(8), intent(in) :: a, b
    integer, intent(in) :: n
    real(8) :: s, h, x
    integer :: i
    h = (b - a) / n
    s = sin(a) + sin(b)
    do i = 1, n - 1
      x = a + i * h
      if (mod(i, 2) == 1) then
        s = s + 4.0d0 * sin(x)
      else
        s = s + 2.0d0 * sin(x)
      end if
    end do
    s = s * h / 3.0d0
  end function
end module
```

Declarations and statements at top level are the loose material of a cell, run
in order. The module of the cell before is in scope:

```fortran
use quadrature
real(8), parameter :: pi = 3.141592653589793d0
real(8) :: integral
integer :: n = 100
integral = simpson(0.0d0, pi, n)
print '(a, f12.9)', 'integral of sin over [0, pi] = ', integral
```

A derived type and an array, then a procedure that works on the array of the
session:

```fortran
type point
  real(8) :: x, y
end type
real(8) :: samples(5)
integer :: k
do k = 1, 5
  samples(k) = k * k
end do
subroutine scale(f)
  real(8), intent(in) :: f
  integer :: j
  do j = 1, 5
    samples(j) = samples(j) * f
  end do
end subroutine
```

The next cell calls it, and `n` still has the value the second cell left:

```fortran
type(point) :: p
call scale(0.5d0)
p%x = samples(5)
p%y = real(n, 8)
print '(a, f6.2, a, f6.1)', 'p = (', p%x, ', ', p%y
print *, sum(samples)
```

`stop` ends the cell, not the session, and the value of `samples` is still there:

```fortran
print *, samples(1)
stop
print *, 'not reached'
```
