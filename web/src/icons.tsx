import type { ComponentType } from 'react'
import {
  ArrowDownToLine,
  Check,
  ChevronDown,
  ChevronRight,
  Clock,
  Copy,
  Folder,
  FolderOpen,
	File,
	Image,
	Paperclip,
  GitFork,
  ListCollapse,
  ListMinus,
  ListTree,
  Moon,
  MoreHorizontal,
  PanelLeft,
  Pencil,
  Pin,
  Plus,
  RefreshCcw,
  Search,
  SendHorizontal,
  Settings,
  SquareSlash,
  Sparkles,
  Square,
  Sun,
  Trash2,
  User,
  Wrench,
  X,
  type LucideProps,
} from 'lucide-react'

type IconProps = Omit<LucideProps, 'size'>

function sized(C: ComponentType<LucideProps>, size: number) {
  return function SizedIcon(props: IconProps) {
    return <C size={size} {...props} />
  }
}

export const IPlus = sized(Plus, 16)
export const IPanel = sized(PanelLeft, 16)
export const ISun = sized(Sun, 16)
export const IMoon = sized(Moon, 16)
export const IGear = sized(Settings, 16)
export const ISend = sized(SendHorizontal, 16)
export const IStop = (props: IconProps) => <Square size={16} fill="currentColor" {...props} />
export const IFork = sized(GitFork, 16)
export const ISearch = sized(Search, 16)
export const IClose = sized(X, 16)
export const IWrench = sized(Wrench, 14)
export const ISpark = sized(Sparkles, 14)
export const IUser = sized(User, 14)
export const ICompact = sized(ListMinus, 14)
export const ICopy = sized(Copy, 14)
export const IEdit = sized(Pencil, 16)
export const IRegen = sized(RefreshCcw, 16)
export const IClock = sized(Clock, 16)
export const IFold = sized(ListCollapse, 16)
export const ITail = sized(ArrowDownToLine, 16)
export const ITraj = sized(ListTree, 16)
export const IPin = sized(Pin, 16)
export const ITrash = sized(Trash2, 16)
export const IDots = sized(MoreHorizontal, 16)
export const IFolder = sized(Folder, 16)
export const IFolderOpen = sized(FolderOpen, 16)
export const IFile = sized(File, 16)
export const IImage = sized(Image, 16)
export const IAttach = sized(Paperclip, 16)
export const ICommand = sized(SquareSlash, 16)
export const IPencil = sized(Pencil, 14)
export const IChevRight = sized(ChevronRight, 12)
export const ICheck = sized(Check, 14)
export const IChevDown = sized(ChevronDown, 16)
export const IChev = ({ open }: { open?: boolean }) => (
  <ChevronRight
    size={12}
    style={{ transform: open ? 'rotate(90deg)' : undefined, transition: 'transform .15s' }}
  />
)
